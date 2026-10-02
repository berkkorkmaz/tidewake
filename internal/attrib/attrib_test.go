package attrib

import (
	"errors"
	"testing"

	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

const me = 501

type fixture struct {
	procs map[int]*proc.Process
	state *harness.State
	self  int
}

func newFixture() *fixture {
	return &fixture{
		procs: map[int]*proc.Process{1: {PID: 1, PGID: 1, UID: 0, Command: "/sbin/launchd"}},
		state: &harness.State{
			Sessions: map[string]*harness.Session{},
			Sources: map[string]error{
				"claude sessions dir": nil, "claude agents --json": nil,
				"codex threads db": nil, "codex chat_processes.json": nil,
			},
			CodexProcesses: map[int]string{},
		},
		self: 99999,
	}
}

func (f *fixture) add(p *proc.Process) *proc.Process {
	if p.UID == 0 {
		p.UID = me
	}
	if p.PGID == 0 {
		p.PGID = p.PID
	}
	if p.Stat == "" {
		p.Stat = "S"
	}
	f.procs[p.PID] = p
	return p
}

func (f *fixture) session(h, id string, alive bool) {
	f.state.Sessions[harness.Key(h, id)] = &harness.Session{Harness: h, ID: id, Alive: alive, Why: "fixture"}
}

func (f *fixture) run(t *testing.T) map[int]Finding {
	t.Helper()
	pack, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]Finding{}
	for _, fd := range Classify(Input{Snap: &proc.Snapshot{Procs: f.procs}, State: f.state, Pack: pack, UID: me, Self: f.self}) {
		out[fd.PID] = fd
	}
	return out
}

func claudeEnv(id string) map[string]string { return map[string]string{"CLAUDE_CODE_SESSION_ID": id} }

func TestChildOfLiveHarnessIsNotFlagged(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 10, PPID: 1, Command: "claude"})
	f.add(&proc.Process{PID: 11, PPID: 10, Command: "node /x/mcp-server.js", Env: claudeEnv("s1")})
	f.session("claude", "s1", false) // even if state says ended, a live parent wins
	if got := f.run(t); len(got) != 0 {
		t.Fatalf("expected nothing flagged, got %+v", got)
	}
}

func TestOrphanedMCPOfEndedSessionIsLeftoverWithItsTree(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 20, PPID: 1, PGID: 15, RSSKB: 1000, Command: "node /x/playwright-mcp.js", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 21, PPID: 20, RSSKB: 500, Command: "/Applications/Chromium --headless"})
	f.session("claude", "dead", false)
	got := f.run(t)
	fd, ok := got[20]
	if !ok || fd.Kind != Leftover {
		t.Fatalf("pid 20 = %+v", fd)
	}
	if fd.TreeRSSKB != 1500 || len(fd.TreePIDs) != 2 || fd.Suggest != "kill -TERM 20 21" {
		t.Fatalf("tree not folded in: %+v", fd)
	}
	if _, child := got[21]; child {
		t.Fatal("child must be folded into its root, not listed twice")
	}
}

func TestDetachedWhenSessionStillRunning(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 30, PPID: 1, Command: "python -m http.server", Env: claudeEnv("alive")})
	f.session("claude", "alive", true)
	fd := f.run(t)[30]
	if fd.Kind != Detached || fd.Suggest != "" {
		t.Fatalf("got %+v", fd)
	}
}

func TestUnknownSessionDependsOnSourceHealth(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 40, PPID: 1, Command: "tail -f log", Env: claudeEnv("never-seen")})
	if got := f.run(t)[40].Kind; got != Leftover {
		t.Fatalf("readable sources: kind = %q, want leftover", got)
	}
	f.state.Sources["claude agents --json"] = errors.New("claude not on PATH")
	if got := f.run(t)[40].Kind; got != Suspect {
		t.Fatalf("unreadable sources: kind = %q, want suspect", got)
	}
}

func TestFallbackSuspectNeedsAllThreeSignals(t *testing.T) {
	scratch := "/private/tmp/claude-501/proj/abc/scratchpad"
	cases := []struct {
		name   string
		p      proc.Process
		leader bool // add a live group leader
		want   bool
	}{
		{"all signals", proc.Process{PID: 50, PPID: 1, PGID: 49, Cwd: scratch, Command: "python x.py"}, false, true},
		{"own group leader", proc.Process{PID: 50, PPID: 1, PGID: 50, Cwd: scratch, Command: "python x.py"}, false, true},
		{"not an agent path", proc.Process{PID: 50, PPID: 1, PGID: 49, Cwd: "/Users/me/app", Command: "python x.py"}, false, false},
		{"leader alive", proc.Process{PID: 50, PPID: 1, PGID: 49, Cwd: scratch, Command: "python x.py"}, true, false},
		{"has a parent", proc.Process{PID: 50, PPID: 7, PGID: 49, Cwd: scratch, Command: "python x.py"}, false, false},
	}
	for _, c := range cases {
		f := newFixture()
		f.add(&proc.Process{PID: 7, PPID: 1, Command: "/bin/zsh"})
		if c.leader {
			f.add(&proc.Process{PID: 49, PPID: 1, Command: "/bin/zsh"})
		}
		p := c.p
		f.add(&p)
		fd, ok := f.run(t)[50]
		if ok != c.want || (ok && fd.Kind != Suspect) {
			t.Errorf("%s: flagged=%v kind=%q, want flagged=%v", c.name, ok, fd.Kind, c.want)
		}
	}
}

func TestStuckHarnessProcess(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 60, PPID: 61, Stat: "E", Command: "codex"})
	f.add(&proc.Process{PID: 61, PPID: 1, Stat: "Es", Command: "/bin/zsh"})
	fd := f.run(t)[60]
	if fd.Kind != Stuck || fd.Harness != "codex" || fd.Suggest != "" {
		t.Fatalf("got %+v", fd)
	}
}

func TestCodexManagedProcessOfEndedThread(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 70, PPID: 1, Command: "npm run dev", Ports: []int{3000}})
	f.state.CodexProcesses[70] = "t1"
	f.session("codex", "t1", false)
	fd := f.run(t)[70]
	if fd.Kind != Leftover || fd.Harness != "codex" || fd.Caution == "" {
		t.Fatalf("got %+v", fd)
	}
}

func TestNeverFlagsOtherUsersSelfOrDesktopAppsWithoutCaution(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 80, PPID: 1, Command: "node x", Env: claudeEnv("dead")})
	f.procs[80].UID = 0 // root-owned; set after add, which defaults UID to me
	f.self = 81
	f.add(&proc.Process{PID: 81, PPID: 1, Command: "tidewake scan", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 82, PPID: 1, Command: "/Applications/Zed.app/Contents/MacOS/zed", Env: claudeEnv("dead")})
	f.session("claude", "dead", false)
	got := f.run(t)
	if _, ok := got[80]; ok {
		t.Error("flagged another user's process")
	}
	if _, ok := got[81]; ok {
		t.Error("flagged itself")
	}
	if got[82].Caution == "" {
		t.Error("desktop app must carry a caution")
	}
}

func TestLaunchdJobIsNeverFlagged(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 90, PPID: 1, Command: "/Applications/OrbStack.app/Contents/MacOS/OrbStack", Env: claudeEnv("dead"), LaunchdJob: true})
	f.session("claude", "dead", false)
	if _, ok := f.run(t)[90]; ok {
		t.Fatal("launchd owns this job; it is not a leftover")
	}
}

func TestDesktopCautionSkipsHeadlessAndNonApps(t *testing.T) {
	cases := map[string]bool{
		"/Applications/Zed.app/Contents/MacOS/zed":                                                            true,
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser --headless=new":                         false,
		"/opt/homebrew/Frameworks/Python.framework/Resources/Python.app/Contents/MacOS/Python -m http.server": false,
	}
	for cmd, want := range cases {
		if got := isDesktopApp(cmd); got != want {
			t.Errorf("isDesktopApp(%q) = %v, want %v", cmd, got, want)
		}
	}
}
