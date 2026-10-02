package attrib

import (
	"errors"
	"strings"
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

func TestServiceIsNeverFlagged(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 90, PPID: 1, Command: "/Applications/OrbStack.app/Contents/MacOS/OrbStack", Env: claudeEnv("dead"), Service: true})
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
		"/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node kernel.js":                            false,
	}
	for cmd, want := range cases {
		if got := isDesktopApp(cmd); got != want {
			t.Errorf("isDesktopApp(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// Review probe: a tmux server from an ended session hosts a live claude
// session; the kill list must stop at the live session.
func TestTreeIsCutAtLiveSession(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 20, PPID: 1, Command: "tmux new-session", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 21, PPID: 20, PGID: 21, Command: "-zsh", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 22, PPID: 21, PGID: 22, Command: "claude", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 23, PPID: 22, PGID: 22, Command: "node mcp.js", Env: claudeEnv("live")})
	f.add(&proc.Process{PID: 24, PPID: 20, PGID: 24, Command: "vite", Env: claudeEnv("other-live")})
	f.session("claude", "dead", false)
	f.session("claude", "live", true)
	f.session("claude", "other-live", true)
	fd := f.run(t)[20]
	if fd.Suggest != "kill -TERM 20 21" {
		t.Fatalf("suggest = %q, want only the dead session's processes", fd.Suggest)
	}
	if !strings.Contains(strings.Join(fd.Proof, ";"), "left out") {
		t.Fatalf("proof must say what was left out: %v", fd.Proof)
	}
}

// Review probe: tidewake run from a shell inside a leftover tree must not
// list its own shell or itself.
func TestSelfAncestorsNeverInKillList(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 50, PPID: 1, Command: "tmux", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 51, PPID: 50, PGID: 51, Command: "-zsh", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 52, PPID: 51, PGID: 52, Command: "tidewake scan", Env: claudeEnv("dead")})
	f.add(&proc.Process{PID: 53, PPID: 50, PGID: 53, Command: "tail -f x", Env: claudeEnv("dead")})
	f.session("claude", "dead", false)
	f.self = 52
	for _, fd := range f.run(t) {
		for _, pid := range fd.TreePIDs {
			if pid == 51 || pid == 52 {
				t.Fatalf("kill list %v includes tidewake's own line", fd.TreePIDs)
			}
		}
	}
}

// Review probe: a scratchpad process without the env var belongs to the
// session named in its path; a live session means detached, not suspect.
func TestScratchpadPathAttribution(t *testing.T) {
	scratch := "/private/tmp/claude-501/-Users-me-proj/c5f4b453-998d-4aab-b9b8-76f615347856/scratchpad"
	f := newFixture()
	f.add(&proc.Process{PID: 60, PPID: 1, PGID: 59, Cwd: scratch, Command: "tail -f out.log"})
	f.session("claude", "c5f4b453-998d-4aab-b9b8-76f615347856", true)
	fd := f.run(t)[60]
	if fd.Kind != Detached || fd.Suggest != "" {
		t.Fatalf("live session: got %+v", fd)
	}
	f.session("claude", "c5f4b453-998d-4aab-b9b8-76f615347856", false)
	if fd := f.run(t)[60]; fd.Kind != Leftover || fd.Suggest != "kill -TERM 60" {
		t.Fatalf("ended session: got %+v", fd)
	}
}

func TestNoCommandWhenUnsureOrServing(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 70, PPID: 1, Command: "node x", Env: claudeEnv("unknown")})
	f.add(&proc.Process{PID: 71, PPID: 1, Command: "postgres -D /data", Env: claudeEnv("dead"), Ports: []int{5432}})
	f.session("claude", "dead", false)
	f.state.Sources["claude agents --json"] = errors.New("old claude")
	got := f.run(t)
	if got[70].Suggest != "" || got[70].Kind != Suspect {
		t.Errorf("unknown liveness must not get a command: %+v", got[70])
	}
	if got[71].Suggest != "" || !strings.Contains(got[71].Caution, ":5432") || !strings.Contains(got[71].Caution, "If not: kill -TERM 71") {
		t.Errorf("a listening service gets the command only inside its note: %+v", got[71])
	}
	if strings.Contains(got[71].Caution, "all network interfaces") {
		t.Errorf("loopback-only service must not get the exposure warning: %+v", got[71])
	}
}

// Review probe: node rewrites its title, so `npm start` above it lacks the
// session id; the kill list must start at the orphaned group root.
func TestClimbsToOrphanedGroupRoot(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 80, PPID: 1, PGID: 80, Command: "npm start"})
	f.add(&proc.Process{PID: 81, PPID: 80, PGID: 80, Command: "node scripts/start.mjs", Env: claudeEnv("dead")})
	f.session("claude", "dead", false)
	got := f.run(t)
	if _, child := got[81]; child {
		t.Fatal("finding must move to the group root")
	}
	fd := got[80]
	if fd.Suggest != "kill -TERM 80 81" {
		t.Fatalf("got %+v", fd)
	}
	proof := strings.Join(fd.Proof, "; ")
	if !strings.Contains(proof, "its child pid 81 carries CLAUDE_CODE_SESSION_ID") || !strings.Contains(proof, "(parent: launchd)") ||
		strings.Contains(proof, "parent: npm") {
		t.Fatalf("proof must describe the root and name the child: %v", fd.Proof)
	}
}

func TestDoesNotClimbIntoAnotherGroup(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 90, PPID: 1, PGID: 90, Command: "-zsh"})
	f.add(&proc.Process{PID: 91, PPID: 90, PGID: 91, Command: "node dev.js", Env: claudeEnv("dead")})
	f.session("claude", "dead", false)
	got := f.run(t)
	if fd, ok := got[91]; !ok || fd.Suggest != "kill -TERM 91" {
		t.Fatalf("got %+v", got)
	}
}

// A child that belongs to a different ended session gets its own row and
// proof instead of riding along in another session's kill list.
func TestOtherEndedSessionGetsItsOwnRow(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 100, PPID: 1, Command: "bash runner.sh", Env: claudeEnv("a")})
	f.add(&proc.Process{PID: 101, PPID: 100, PGID: 101, Command: "node server.js", Env: claudeEnv("b")})
	f.session("claude", "a", false)
	f.session("claude", "b", false)
	got := f.run(t)
	if got[100].Suggest != "kill -TERM 100" || got[101].Suggest != "kill -TERM 101" || got[101].SessionID != "b" {
		t.Fatalf("got a=%+v b=%+v", got[100], got[101])
	}
}

// A running session's headless browser is one row, not one per helper.
func TestDetachedTreeIsOneRow(t *testing.T) {
	scratch := "--user-data-dir=/private/tmp/claude-501/-Users-me-p/c5f4b453-998d-4aab-b9b8-76f615347856/scratchpad/profile"
	f := newFixture()
	f.add(&proc.Process{PID: 10, PPID: 1, PGID: 9, RSSKB: 200, Command: "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser --headless=new " + scratch})
	f.add(&proc.Process{PID: 11, PPID: 10, PGID: 9, RSSKB: 100, Command: "/Applications/Brave Browser.app/Contents/Frameworks/Helper --type=renderer " + scratch})
	f.add(&proc.Process{PID: 12, PPID: 10, PGID: 9, RSSKB: 50, Command: "/Applications/Brave Browser.app/Contents/Frameworks/Helper --type=gpu " + scratch})
	f.session("claude", "c5f4b453-998d-4aab-b9b8-76f615347856", true)
	got := f.run(t)
	if len(got) != 1 || got[10].Kind != Detached || got[10].TreeRSSKB != 350 || got[10].Suggest != "" {
		t.Fatalf("got %+v", got)
	}
}

// Colleague report: orphaned Codex sandbox kernels inside ChatGPT.app were
// treated as the harness itself and never listed.
func TestOrphanedHarnessHelperWithMarkerIsSuspect(t *testing.T) {
	f := newFixture()
	kernel := "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node --experimental-vm-modules /tmp/x/kernel.js --session-id abc"
	f.add(&proc.Process{PID: 55510, PPID: 1, PGID: 2206, Command: kernel, Env: map[string]string{"CODEX_SANDBOX": "seatbelt"}})
	f.add(&proc.Process{PID: 3000, PPID: 1, Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"})
	f.add(&proc.Process{PID: 3001, PPID: 3000, PGID: 3000, Command: kernel, Env: map[string]string{"CODEX_SANDBOX": "seatbelt"}})
	got := f.run(t)
	fd, ok := got[55510]
	if !ok || fd.Kind != Suspect || fd.Harness != "codex" || fd.Suggest != "kill -TERM 55510" {
		t.Fatalf("orphaned kernel: %+v", fd)
	}
	if _, ok := got[3001]; ok {
		t.Fatal("a kernel under the running app must not be flagged")
	}
	if _, ok := got[3000]; ok {
		t.Fatal("the app itself must not be flagged")
	}
}

func TestExposedPortWarning(t *testing.T) {
	f := newFixture()
	f.add(&proc.Process{PID: 40, PPID: 1, Command: "python -m http.server 8765", Env: claudeEnv("live"), Ports: []int{8765}, Exposed: []int{8765}})
	f.session("claude", "live", true)
	fd := f.run(t)[40]
	if fd.Kind != Detached || !strings.Contains(fd.Caution, ":8765 listens on all network interfaces") {
		t.Fatalf("got %+v", fd)
	}
}
