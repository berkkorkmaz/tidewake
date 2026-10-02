package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

var (
	now      = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	started  = time.Date(2026, 9, 29, 7, 55, 55, 0, time.UTC)
	interval = 3 * time.Second
)

const gb = int64(1e9) / bytesPerKiB // KiB in a decimal GB

type session struct {
	status      string
	statusAgo   time.Duration
	activityAgo time.Duration // 0 means no transcript
	rssKB       int64
	cpuInSample time.Duration
}

// run builds one Claude session (pid 100) with an MCP child (pid 101).
func run(t *testing.T, s session) Row {
	t.Helper()
	pack, err := rules.Load()
	if err != nil {
		t.Fatal(err)
	}
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{
		100: {PID: 100, PPID: 1, Command: "claude", Started: started, RSSKB: s.rssKB / 2, CPUTime: time.Minute},
		101: {PID: 101, PPID: 100, Command: "node mcp-server.js", Started: started, RSSKB: s.rssKB / 2, CPUTime: time.Minute},
	}}
	after := map[int]time.Duration{100: time.Minute + s.cpuInSample, 101: time.Minute}
	rows := Report(Input{
		Snap: snap, CPUAfter: after, Interval: interval, Pack: pack, Limits: DefaultThresholds, Now: now,
		Sessions: []ClaudeSession{{PID: 100, SessionID: "s1", Status: s.status, Kind: "interactive",
			StatusUpdatedAt: now.Add(-s.statusAgo).UnixMilli(), ProcStart: started.Format(proc.LstartLayout)}},
		Activity: func(string) time.Time {
			if s.activityAgo == 0 {
				return time.Time{}
			}
			return now.Add(-s.activityAgo)
		},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	return rows[0]
}

func hasFlag(r Row, kind string) bool {
	for _, f := range r.Flags {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func TestStuckNeedsAllThreeSignals(t *testing.T) {
	base := session{status: "busy", statusAgo: 77 * time.Hour, activityAgo: 77 * time.Hour, rssKB: gb}
	if r := run(t, base); !hasFlag(r, FlagStuck) || len(r.Flags[0].Proof) != 3 {
		t.Fatalf("all signals: %+v", r.Flags)
	}
	cases := map[string]func(*session){
		"working CPU":         func(s *session) { s.cpuInSample = 300 * time.Millisecond }, // 10%
		"recent activity":     func(s *session) { s.activityAgo = 10 * time.Minute },
		"busy only briefly":   func(s *session) { s.statusAgo = 30 * time.Minute },
		"idle, not busy":      func(s *session) { s.status = "idle" },
		"no transcript known": func(s *session) { s.activityAgo = 0 },
	}
	for name, mutate := range cases {
		s := base
		mutate(&s)
		if r := run(t, s); hasFlag(r, FlagStuck) {
			t.Errorf("%s: must not be flagged stuck", name)
		}
	}
}

func TestIdleHolding(t *testing.T) {
	s := session{status: "idle", statusAgo: 31 * time.Hour, activityAgo: 31 * time.Hour, rssKB: 14 * gb / 10}
	if !hasFlag(run(t, s), FlagIdleHold) {
		t.Fatal("idle a day holding 1.4 GB must be flagged")
	}
	s.rssKB = gb / 2
	if hasFlag(run(t, s), FlagIdleHold) {
		t.Fatal("holding 0.5 GB is not worth a flag")
	}
	s.rssKB, s.activityAgo = 14*gb/10, 3*time.Hour
	if hasFlag(run(t, s), FlagIdleHold) {
		t.Fatal("idle 3h is normal")
	}
}

func TestSpinningWhileIdle(t *testing.T) {
	s := session{status: "idle", statusAgo: time.Hour, activityAgo: time.Hour, rssKB: gb, cpuInSample: 2400 * time.Millisecond} // 80%
	r := run(t, s)
	if !hasFlag(r, FlagSpinning) || r.CPUPercent < 79 || r.CPUPercent > 81 {
		t.Fatalf("got cpu=%.1f flags=%+v", r.CPUPercent, r.Flags)
	}
	s.statusAgo = time.Minute
	if hasFlag(run(t, s), FlagSpinning) {
		t.Fatal("CPU right after going idle is finishing up, not spinning")
	}
}

func TestHighRAMNamesBiggestPart(t *testing.T) {
	r := run(t, session{status: "busy", statusAgo: time.Minute, activityAgo: time.Minute, rssKB: 6 * gb})
	if !hasFlag(r, FlagHighRAM) || r.Biggest == nil || r.Biggest.PID != 101 {
		t.Fatalf("got %+v", r)
	}
	if hasFlag(run(t, session{status: "busy", statusAgo: time.Minute, activityAgo: time.Minute, rssKB: 3 * gb}), FlagHighRAM) {
		t.Fatal("3 GB is under the default 4 GB")
	}
}

func TestDeadOrReusedSessionHasNoRow(t *testing.T) {
	pack, _ := rules.Load()
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{100: {PID: 100, Command: "zsh", Started: started.Add(time.Hour)}}}
	rows := Report(Input{Snap: snap, Pack: pack, Limits: DefaultThresholds, Now: now, Sessions: []ClaudeSession{
		{PID: 100, SessionID: "reused", ProcStart: started.Format(proc.LstartLayout)},
		{PID: 200, SessionID: "gone", ProcStart: started.Format(proc.LstartLayout)},
	}})
	if len(rows) != 0 {
		t.Fatalf("got %+v", rows)
	}
}

func TestCodexRowsAreTopLevelOnly(t *testing.T) {
	pack, _ := rules.Load()
	snap := &proc.Snapshot{Procs: map[int]*proc.Process{
		10: {PID: 10, PPID: 1, Command: "/Users/me/.codex/packages/app-server-daemon/releases/0.160.0/bin/codex app-server", RSSKB: 100},
		11: {PID: 11, PPID: 10, Command: "/Users/me/.codex/packages/app-server-daemon/releases/0.160.0/bin/codex exec", RSSKB: 50},
		12: {PID: 12, PPID: 10, Command: "node github-mcp", RSSKB: 25},
	}}
	rows := Report(Input{Snap: snap, Pack: pack, Limits: DefaultThresholds, Now: now})
	if len(rows) != 1 || rows[0].PID != 10 || rows[0].TreeRSSKB != 175 {
		t.Fatalf("got %+v", rows)
	}
}

func TestTranscriptActivityIncludesSubagents(t *testing.T) {
	projects := t.TempDir()
	main := filepath.Join(projects, "-Users-me-app", "s1.jsonl")
	sub := filepath.Join(projects, "-Users-me-app", "s1", "subagents", "agent-a.jsonl")
	for _, p := range []string{main, sub} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, newer := now.Add(-5*time.Hour), now.Add(-time.Hour)
	_ = os.Chtimes(main, old, old)
	_ = os.Chtimes(sub, newer, newer)
	activity := TranscriptActivity(projects)
	if got := activity("s1"); !got.Equal(newer) {
		t.Fatalf("got %v, want the subagent's newer write %v", got, newer)
	}
	if !activity("unknown").IsZero() {
		t.Fatal("unknown session must have no activity")
	}
}
