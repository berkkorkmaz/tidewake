package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// started is the instant written as procStart "Tue Sep 29 07:55:55 2026" (UTC).
var started = time.Date(2026, 9, 29, 7, 55, 55, 0, time.UTC)

// withLocalZone runs the test as if the machine were in UTC+3, so a UTC/local
// mix-up cannot pass on a UTC CI runner.
func withLocalZone(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.FixedZone("UTC+3", 3*60*60)
	t.Cleanup(func() { time.Local = prev })
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeRunner(outputs map[string]string, errs map[string]error) Runner {
	return func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if err := errs[name]; err != nil {
			return nil, err
		}
		return []byte(outputs[name]), nil
	}
}

func load(t *testing.T, claudeHome, codexHome string, run Runner, snap *proc.Snapshot) *State {
	t.Helper()
	pack, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	return Load(context.Background(), Options{ClaudeHome: claudeHome, CodexHome: codexHome, Run: run, Now: now}, snap, pack)
}

func TestClaudeSessionFileLivenessUsesProcStart(t *testing.T) {
	withLocalZone(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "sessions", "100.json"),
		`{"pid":100,"sessionId":"live","procStart":"Tue Sep 29 07:55:55 2026"}`)
	writeFile(t, filepath.Join(home, "sessions", "200.json"),
		`{"pid":200,"sessionId":"reused","procStart":"Tue Sep 29 07:55:55 2026"}`)
	writeFile(t, filepath.Join(home, "sessions", "300.json"),
		`{"pid":300,"sessionId":"gone","procStart":"Tue Sep 29 07:55:55 2026"}`)
	writeFile(t, filepath.Join(home, "sessions", "bad.json"), `{not json`)
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{
		100: {PID: 100, Started: started},
		200: {PID: 200, Started: started.Add(time.Hour)}, // PID reused by another process
	}}
	st := load(t, home, t.TempDir(), fakeRunner(map[string]string{"claude": "[]"}, nil), snap)

	for id, want := range map[string]bool{"live": true, "reused": false, "gone": false} {
		s := st.Lookup("claude", id)
		if s == nil || s.Alive != want {
			t.Errorf("%s: got %+v, want alive=%v", id, s, want)
		}
	}
}

func TestClaudeAgentsStates(t *testing.T) {
	out := `[{"sessionId":"a","state":"working"},{"sessionId":"b","state":"done"},
	         {"sessionId":"c","state":"some-new-state"},{"sessionId":"d","pid":999,"status":"idle"},
	         {"sessionId":"e","pid":100,"status":"idle"}]`
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{100: {PID: 100}}}
	st := load(t, t.TempDir(), t.TempDir(), fakeRunner(map[string]string{"claude": out}, nil), snap)
	want := map[string]bool{"a": true, "b": false, "c": true, "d": false, "e": true}
	for id, alive := range want {
		if s := st.Lookup("claude", id); s == nil || s.Alive != alive {
			t.Errorf("%s: got %+v, want alive=%v", id, s, alive)
		}
	}
}

func TestAliveSourceWinsOverDead(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "sessions", "100.json"), `{"pid":100,"sessionId":"x"}`)
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{100: {PID: 100}}}
	st := load(t, home, t.TempDir(), fakeRunner(map[string]string{"claude": `[{"sessionId":"x","state":"done"}]`}, nil), snap)
	if !st.Lookup("claude", "x").Alive {
		t.Fatal("a running session process must keep the session alive")
	}
}

func TestMissingSourcesAreRecordedNotFatal(t *testing.T) {
	run := fakeRunner(nil, map[string]error{"claude": errors.New("not installed"), "sqlite3": errors.New("x")})
	st := load(t, filepath.Join(t.TempDir(), "nope"), t.TempDir(), run, &proc.Snapshot{Procs: map[int]*proc.Process{}})
	for _, src := range []string{"claude sessions dir", "claude agents --json", "codex threads db", "codex chat_processes.json"} {
		if st.Sources[src] == nil {
			t.Errorf("%s: expected an error to be recorded", src)
		}
	}
}

func TestCodexThreadLiveness(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour).Unix()
	old := now.Add(-48 * time.Hour).Unix()
	cases := []struct {
		name    string
		thread  codexThread
		running bool
		alive   bool
	}{
		{"recent and running", codexThread{UpdatedAt: recent}, true, true},
		{"archived", codexThread{UpdatedAt: recent, Archived: 1}, true, false},
		{"codex not running", codexThread{UpdatedAt: recent}, false, false},
		{"idle too long", codexThread{UpdatedAt: old}, true, false},
	}
	for _, c := range cases {
		if got, _ := codexThreadAlive(c.thread, c.running, now, DefaultCodexIdle); got != c.alive {
			t.Errorf("%s: alive=%v want %v", c.name, got, c.alive)
		}
	}
}

func TestCodexSourcesFromFixtures(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "")
	procStarted := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(home, "process_manager", "chat_processes.json"),
		`[{"osPid":4242,"conversationId":"t1","startedAtMs":`+strconv.FormatInt(procStarted.UnixMilli()+400, 10)+`},
		  {"osPid":5151,"conversationId":"old","startedAtMs":`+strconv.FormatInt(procStarted.Add(-30*24*time.Hour).UnixMilli(), 10)+`},
		  {"osPid":6161,"conversationId":"nostart"}]`)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	threads := `[{"id":"t1","cwd":"/r","archived":0,"updated_at":` + strconv.FormatInt(now.Add(-time.Hour).Unix(), 10) + `}]`
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{
		7:    {PID: 7, Command: "/Users/me/.codex/packages/standalone/current/codex"},
		4242: {PID: 4242, Started: procStarted},
		5151: {PID: 5151, Started: procStarted}, // PID reused since the stale entry was written
		6161: {PID: 6161, Started: procStarted},
	}}
	st := load(t, t.TempDir(), home, fakeRunner(map[string]string{"claude": "[]", "sqlite3": threads}, nil), snap)
	if s := st.Lookup("codex", "t1"); s == nil || !s.Alive {
		t.Fatalf("t1 = %+v", s)
	}
	if st.CodexProcesses[4242] != "t1" || len(st.CodexProcesses) != 1 {
		t.Fatalf("chat processes = %v", st.CodexProcesses)
	}
}

func TestCodexStateDBPicksHighestGeneration(t *testing.T) {
	home := t.TempDir()
	for _, n := range []string{"state_9.sqlite", "state_10.sqlite", "state_x.sqlite"} {
		writeFile(t, filepath.Join(home, n), "")
	}
	got, err := codexStateDB(home)
	if err != nil || filepath.Base(got) != "state_10.sqlite" {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestCodexThreadsFallBackToImmutableOpen(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "")
	var calls [][]string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "claude" {
			return []byte("[]"), nil
		}
		calls = append(calls, args)
		if args[0] == "-readonly" {
			return nil, errors.New("unable to open database file (14)")
		}
		return []byte(`[{"id":"t1","cwd":"/r","archived":1,"updated_at":1}]`), nil
	}
	st := load(t, t.TempDir(), home, run, &proc.Snapshot{Procs: map[int]*proc.Process{}})
	if st.Sources["codex threads db"] != nil || st.Lookup("codex", "t1") == nil {
		t.Fatalf("fallback not used: sources=%v calls=%v", st.Sources, calls)
	}
	if len(calls) != 2 || !strings.Contains(calls[1][1], "immutable=1") {
		t.Fatalf("calls = %v", calls)
	}
}

// Week-long parallel runs: a thread idle for 3 days is alive under --codex-idle 7d.
func TestCodexIdleLimitIsConfigurable(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	th := codexThread{UpdatedAt: now.Add(-72 * time.Hour).Unix()}
	if alive, _ := codexThreadAlive(th, true, now, DefaultCodexIdle); alive {
		t.Fatal("3 days idle is past the 24h default")
	}
	if alive, _ := codexThreadAlive(th, true, now, 7*24*time.Hour); !alive {
		t.Fatal("3 days idle must count as alive under a 7-day limit")
	}
	if got := codexIdle(Options{}); got != DefaultCodexIdle {
		t.Fatalf("zero option must mean the default, got %v", got)
	}
}
