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

func TestVersionCheck(t *testing.T) {
	pack, err := rules.Parse([]byte(testPack))
	if err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "claude" {
			return []byte("2.1.250 (Claude Code)\n"), nil
		}
		return nil, errors.New("not installed")
	}
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
	if len(got) != 1 || got[0].Sessions != 2 || got[0].RSSKB != 250 {
		t.Fatalf("got %+v", got)
	}
}
