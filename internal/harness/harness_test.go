package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

var started = time.Date(2026, 9, 29, 7, 55, 55, 0, time.Local)

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
		if got, _ := codexThreadAlive(c.thread, c.running, now); got != c.alive {
			t.Errorf("%s: alive=%v want %v", c.name, got, c.alive)
		}
	}
}

func TestCodexSourcesFromFixtures(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "state_5.sqlite"), "")
	writeFile(t, filepath.Join(home, "process_manager", "chat_processes.json"),
		`[{"osPid":4242,"conversationId":"t1","command":"npm run dev"}]`)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	threads := `[{"id":"t1","cwd":"/r","archived":0,"updated_at":` + strconv.FormatInt(now.Add(-time.Hour).Unix(), 10) + `}]`
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{7: {PID: 7, Command: "/Users/me/.codex/packages/standalone/current/codex"}}}
	st := load(t, t.TempDir(), home, fakeRunner(map[string]string{"claude": "[]", "sqlite3": threads}, nil), snap)
	if s := st.Lookup("codex", "t1"); s == nil || !s.Alive {
		t.Fatalf("t1 = %+v", s)
	}
	if st.CodexProcesses[4242] != "t1" {
		t.Fatalf("chat processes = %v", st.CodexProcesses)
	}
}
