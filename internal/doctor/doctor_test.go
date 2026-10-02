package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

const testPack = `{
  "version": "t1",
  "harnesses": {
    "claude": {"label": "Claude Code", "versionCommand": ["claude", "--version"], "processNames": ["claude"], "sessionEnv": "X"},
    "codex":  {"label": "Codex", "versionCommand": ["codex", "--version"], "processNames": ["codex"], "sessionEnv": "Y"}
  },
  "knownIssues": [
    {"harness": "claude", "status": "fixed", "fixedIn": "2.1.283", "title": "fixed later", "url": "u1"},
    {"harness": "claude", "status": "fixed", "fixedIn": "2.1.200", "title": "fixed earlier", "url": "u2"},
    {"harness": "claude", "status": "open", "title": "still open", "url": "u3"}
  ]
}`

func fakeInstalls(t *testing.T, onPath map[string]string, versions map[string]string) harness.Runner {
	t.Helper()
	prev := lookPath
	lookPath = func(name string) (string, error) {
		if p, ok := onPath[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = prev })
	return func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if v, ok := versions[name]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("no such binary")
	}
}

func TestVersionCheck(t *testing.T) {
	pack, err := rules.Parse([]byte(testPack))
	if err != nil {
		t.Fatal(err)
	}
	run := fakeInstalls(t, map[string]string{"claude": "/bin/claude"}, map[string]string{"/bin/claude": "2.1.250 (Claude Code)\n"})
	st := &harness.State{Sources: map[string]error{"claude agents --json": nil, "codex threads db": errors.New("no db")}}
	r := Run(context.Background(), pack, run, &proc.Snapshot{Procs: map[int]*proc.Process{}}, st)

	c, x := r.Harnesses[0], r.Harnesses[1]
	if c.Name != "claude" || !c.Installed || c.Version != "2.1.250" {
		t.Fatalf("claude = %+v", c)
	}
	if len(c.Affected) != 1 || c.Affected[0].URL != "u1" || c.Fixed != 1 || len(c.Open) != 1 {
		t.Fatalf("claude issues = %+v", c)
	}
	if x.Installed {
		t.Fatal("codex must report not installed")
	}
	if r.Sources["claude agents --json"] != "ok" || r.Sources["codex threads db"] != "no db" {
		t.Fatalf("sources = %v", r.Sources)
	}
}

// Teammate report: a stale Homebrew CLI on PATH hid the newer Codex the app runs.
func TestRunningInstallWinsOverStalePath(t *testing.T) {
	pack, _ := rules.Parse([]byte(`{
	  "version": "t", "harnesses": {"codex": {"label": "Codex", "versionCommand": ["codex", "--version"], "processNames": ["codex"], "sessionEnv": "Y"}},
	  "knownIssues": [
	    {"harness": "codex", "status": "fixed", "fixedIn": "0.148.0", "title": "old fix", "url": "a"},
	    {"harness": "codex", "status": "fixed", "fixedIn": "0.158.0", "title": "new fix", "url": "b"}]}`))
	app := "/Applications/ChatGPT.app/Contents/Resources/codex"
	run := fakeInstalls(t, map[string]string{"codex": "/opt/homebrew/bin/codex"}, map[string]string{
		"/opt/homebrew/bin/codex": "codex-cli 0.25.0", app: "codex-cli 0.155.0-alpha.9.2"})
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{9: {PID: 9, Command: app + " app-server", Exe: app}}}
	r := Run(context.Background(), pack, run, snap, &harness.State{Sources: map[string]error{}})
	h := r.Harnesses[0]
	if h.Version != "0.155.0" || len(h.Affected) != 1 || h.Affected[0].URL != "b" || len(h.Installs) != 2 {
		t.Fatalf("got %+v", h)
	}
}

func TestDuplicateMCP(t *testing.T) {
	pack, _ := rules.Parse([]byte(testPack))
	mcp := "node /Users/me/.npm/_npx/x/node_modules/.bin/playwright-mcp"
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{
		10: {PID: 10, PPID: 1, Command: "claude"},
		20: {PID: 20, PPID: 1, Command: "claude"},
		11: {PID: 11, PPID: 10, Command: mcp, RSSKB: 100},
		12: {PID: 12, PPID: 11, Command: "chrome", RSSKB: 50},
		21: {PID: 21, PPID: 20, Command: mcp, RSSKB: 100},
		13: {PID: 13, PPID: 10, Command: "uvx some-mcp-server", RSSKB: 70}, // only one session
		30: {PID: 30, PPID: 1, Command: mcp, RSSKB: 999},                   // orphan, not under a harness
	}}
	got := DuplicateMCP(snap, pack)
	if len(got) != 1 || got[0].Copies != 2 || got[0].Sessions != 2 || got[0].RSSKB != 250 {
		t.Fatalf("got %+v", got)
	}
	// Codex runs one copy per thread under a single app-server.
	snap.Procs[40] = &proc.Process{PID: 40, PPID: 1, Command: "codex app-server"}
	snap.Procs[41] = &proc.Process{PID: 41, PPID: 40, Command: "node github-mcp", RSSKB: 10}
	snap.Procs[42] = &proc.Process{PID: 42, PPID: 40, Command: "node github-mcp", RSSKB: 10}
	got = DuplicateMCP(snap, pack)
	if len(got) != 2 || got[1].Copies != 2 || got[1].Sessions != 1 {
		t.Fatalf("per-thread copies under one parent: %+v", got)
	}
}
