package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/health"
)

func TestSessionsReport(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	rows := []health.Row{
		{Harness: "claude", PID: 4242, SessionID: "4e206b9e-0000", Name: "leus-dbt-76", Status: "busy", Started: now.Add(-72 * time.Hour),
			TreeRSSKB: 84480, LastActivity: now.Add(-72 * time.Hour),
			Flags: []health.Flag{{Kind: health.FlagStuck, Text: "looks stuck: busy for 3d with no activity", Proof: []string{"a", "b", "c"}, Advice: "check its terminal"}}},
		{Harness: "codex", PID: 77, Command: "/Users/me/.codex/packages/x/bin/codex app-server", Started: now.Add(-time.Hour)},
	}
	var buf bytes.Buffer
	Sessions(&buf, rows, SessionsOptions{Now: now, Limits: health.DefaultThresholds, Sample: 3 * time.Second})
	out := buf.String()
	for _, want := range []string{"1 Claude Code · 1 Codex", "1 need a look", "pid 4242", "4e206b9e leus-dbt-76", "Looks stuck: busy for 3d",
		"why: a; b; c", "tip: check its terminal", "pid 77", "codex app-server", "last activity 3d ago", "Read-only: nothing was stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
	buf.Reset()
	Sessions(&buf, nil, SessionsOptions{Now: now, Limits: health.DefaultThresholds, Sample: time.Second})
	if !strings.Contains(buf.String(), "none running") {
		t.Error("empty report must say none running")
	}
}
