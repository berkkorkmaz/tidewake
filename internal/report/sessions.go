package report

import (
	"io"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/health"
)

// SessionsOptions control the health report text.
type SessionsOptions struct {
	Home   string
	Now    time.Time
	Limits health.Thresholds
	Sample time.Duration
}

// Sessions writes the live-session health report.
func Sessions(w io.Writer, rows []health.Row, opts SessionsOptions) {
	p := printer{w: w, home: opts.Home}
	counts := map[string]int{}
	var total int64
	flagged := 0
	for _, r := range rows {
		counts[r.Harness]++
		total += r.TreeRSSKB
		if len(r.Flags) > 0 {
			flagged++
		}
	}
	p.line("LIVE SESSIONS  %d Claude Code · %d Codex · %s RAM in total · %d need a look",
		counts["claude"], counts["codex"], mem(total), flagged)
	if len(rows) == 0 {
		p.line("  none running")
	}
	for _, r := range rows {
		p.line("  %-6s pid %-6d %-30s %-6s %5s  %8s  %4.0f%%  %s", r.Harness, r.PID, sessionLabel(p, r), dash(r.Status),
			age(opts.Now, r.Started), mem(r.TreeRSSKB), r.CPUPercent, activityText(opts.Now, r.LastActivity))
		for _, f := range r.Flags {
			p.line("         %s", strings.ToUpper(f.Text[:1])+f.Text[1:])
			p.line("           why: %s", strings.Join(f.Proof, "; "))
			if f.Advice != "" {
				p.line("           tip: %s", f.Advice)
			}
		}
	}
	p.line("")
	p.line("Flags: stuck after %s busy with no activity · idle after %s · memory over %s · CPU sampled over %s",
		dur(opts.Limits.Stuck), dur(opts.Limits.Idle), mem(opts.Limits.RAMKB), opts.Sample.Round(time.Second))
	p.line("Read-only: nothing was stopped. Change limits with --stuck, --idle and --ram.")
}

func sessionLabel(p printer, r health.Row) string {
	if r.Harness != "claude" {
		return p.truncateTo(displayCommand(r.Command), 30)
	}
	id := r.SessionID
	if len(id) > 8 {
		id = id[:8]
	}
	label := id
	if r.Name != "" {
		label += " " + r.Name
	}
	return p.truncateTo(label, 30)
}

func activityText(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "last activity " + dur(now.Sub(t)) + " ago"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (p printer) truncateTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
