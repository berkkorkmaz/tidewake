// Package report renders scan and doctor results for people.
package report

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/attrib"
	"github.com/berkkorkmaz/tidewake/internal/disk"
	"github.com/berkkorkmaz/tidewake/internal/doctor"
	"github.com/berkkorkmaz/tidewake/internal/scan"
	"github.com/berkkorkmaz/tidewake/internal/worktree"
)

const (
	commandWidth   = 72
	maxKeptReasons = 4
	kb             = 1024
)

// Options control what the text report includes.
type Options struct {
	All  bool   // include kept worktrees and detached processes
	Home string // replaced by ~ in paths
}

// Scan writes the human report for a scan.
func Scan(w io.Writer, r *scan.Result, opts Options) {
	p := printer{w: w, home: opts.Home}
	p.line("tidewake scan · %s · rules %s", r.Taken.Format("2006-01-02 15:04"), r.RulesVersion)
	p.processes(r, opts.All)
	p.worktrees(r.Worktrees, opts.All)
	p.disk(r.Disk)
	p.totals(r)
	p.sources(r.Sources)
	p.line("")
	p.line("Nothing was changed: scan is read-only. Commands use PIDs from this scan; rescan before running them later.")
}

type printer struct {
	w    io.Writer
	home string
}

func (p printer) line(format string, a ...any) { fmt.Fprintf(p.w, format+"\n", a...) }

func (p printer) path(s string) string {
	if p.home == "" {
		return s
	}
	if s == p.home {
		return "~"
	}
	// Match whole path segments so /Users/bobby is not shortened for home /Users/bob.
	return strings.ReplaceAll(s, p.home+"/", "~/")
}

func (p printer) processes(r *scan.Result, all bool) {
	var shown []attrib.Finding
	var ram int64
	for _, f := range r.Processes {
		if f.Kind == attrib.Detached && !all {
			continue
		}
		shown = append(shown, f)
		if f.Kind == attrib.Leftover || f.Kind == attrib.Suspect {
			ram += f.TreeRSSKB
		}
	}
	p.line("")
	p.line("PROCESSES LEFT BEHIND  %d found · %s RAM in leftovers and suspects", len(shown), mem(ram))
	if len(shown) == 0 {
		p.line("  none")
	}
	for _, f := range shown {
		p.line("  %-8s %-6s %-22s pid %-6d %5s  %8s  %s", f.Kind, f.Harness, session(f), f.PID,
			age(r.Taken, f.Started), mem(f.TreeRSSKB), p.truncate(f.Command))
		if len(f.TreePIDs) > 1 {
			p.line("           tree: %d processes", len(f.TreePIDs))
		}
		p.line("           why:  %s", strings.Join(f.Proof, "; "))
		if f.Caution != "" {
			p.line("           note: %s", f.Caution)
		}
		if f.Suggest != "" {
			p.line("           run:  %s", f.Suggest)
		}
	}
	if hidden := countKind(r.Processes, attrib.Detached); hidden > 0 && !all {
		p.line("  + %d detached process(es) whose session is still running (--all to list)", hidden)
	}
}

func (p printer) worktrees(wts []worktree.Worktree, all bool) {
	var removable, dangling, kept []worktree.Worktree
	var bytes int64
	for _, w := range wts {
		switch w.Kind {
		case worktree.Removable:
			removable = append(removable, w)
			bytes += w.SizeBytes
		case worktree.Dangling:
			dangling = append(dangling, w)
		default:
			kept = append(kept, w)
		}
	}
	p.line("")
	p.line("WORKTREES  %d removable (%s) · %d dangling · %d kept", len(removable), size(bytes), len(dangling), len(kept))
	for _, w := range removable {
		p.line("  removable %-6s %-48s %8s  (%s rebuildable)  idle %s", w.Owner, p.path(w.Path),
			size(w.SizeBytes), size(w.RegenBytes), dur(w.Idle))
		p.line("            run: %s", p.path(w.Suggest))
	}
	for _, repo := range danglingRepos(dangling) {
		p.line("  dangling  %d registration(s) in %s point to deleted folders", repo.count, p.path(repo.path))
		p.line("            run: %s", p.path(repo.suggest))
	}
	if all {
		for _, w := range kept {
			p.line("  kept      %-6s %-48s %8s  %s", w.Owner, p.path(w.Path), size(w.SizeBytes), strings.Join(w.Reasons, "; "))
		}
	} else if len(kept) > 0 {
		p.line("  kept: %s (--all to list)", topReasons(kept))
	}
}

type repoCount struct {
	path, suggest string
	count         int
}

func danglingRepos(ws []worktree.Worktree) []repoCount {
	byRepo := map[string]*repoCount{}
	for _, w := range ws {
		rc := byRepo[w.Repo]
		if rc == nil {
			rc = &repoCount{path: w.Repo, suggest: w.Suggest}
			byRepo[w.Repo] = rc
		}
		rc.count++
	}
	out := make([]repoCount, 0, len(byRepo))
	for _, rc := range byRepo {
		out = append(out, *rc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// topReasons summarises why worktrees were kept, most common first.
func topReasons(kept []worktree.Worktree) string {
	counts := map[string]int{}
	for _, w := range kept {
		for _, r := range w.Reasons {
			counts[reasonClass(r)]++
		}
	}
	type rc struct {
		reason string
		n      int
	}
	var list []rc
	for r, n := range counts {
		list = append(list, rc{r, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].reason < list[j].reason
	})
	if len(list) > maxKeptReasons {
		list = list[:maxKeptReasons]
	}
	parts := make([]string, len(list))
	for i, x := range list {
		parts[i] = fmt.Sprintf("%s %d", x.reason, x.n)
	}
	return strings.Join(parts, ", ")
}

// reasonClass strips specifics (counts, pids, ages) so reasons group.
func reasonClass(r string) string {
	switch {
	case strings.HasPrefix(r, "in use"):
		return "in use"
	case strings.HasPrefix(r, "touched"):
		return "touched recently"
	case strings.Contains(r, "not on any remote"):
		return "unpushed commits"
	case strings.HasPrefix(r, "locked"):
		return "locked"
	case strings.Contains(r, "uncommitted"):
		return "uncommitted files"
	case strings.HasPrefix(r, "keeps ignored files"):
		return "ignored files"
	}
	return r
}

func (p printer) disk(items []disk.Item) {
	var reclaim int64
	for _, it := range items {
		reclaim += it.Reclaimable
	}
	p.line("")
	p.line("DISK  %s reclaimable", size(reclaim))
	for _, it := range items {
		if it.Bytes == 0 {
			continue
		}
		label := it.Category
		if it.Path != "" && it.Category == "codex old release" {
			label += " " + lastElem(it.Path)
		}
		reclaimText := "info only"
		if it.Reclaimable > 0 {
			reclaimText = size(it.Reclaimable) + " reclaimable"
		}
		p.line("  %-36s %9s  %s", label, size(it.Bytes), reclaimText)
		if it.Note != "" {
			p.line("      note: %s", it.Note)
		}
		if it.Suggest != "" && it.Reclaimable > 0 {
			p.line("      run:  %s", p.path(it.Suggest))
		}
	}
}

func (p printer) totals(r *scan.Result) {
	var ram, bytes int64
	for _, f := range r.Processes {
		if f.Kind == attrib.Leftover || f.Kind == attrib.Suspect {
			ram += f.TreeRSSKB
		}
	}
	for _, w := range r.Worktrees {
		if w.Kind == worktree.Removable {
			bytes += w.SizeBytes
		}
	}
	for _, it := range r.Disk {
		bytes += it.Reclaimable
	}
	p.line("")
	p.line("TOTAL  %s disk and %s RAM can be reclaimed", size(bytes), mem(ram))
}

func (p printer) sources(src map[string]string) {
	var bad []string
	for name, status := range src {
		if status != "ok" {
			bad = append(bad, name)
		}
	}
	if len(bad) == 0 {
		return
	}
	sort.Strings(bad)
	p.line("")
	p.line("Not read (not installed or not running): %s. Run `tidewake doctor` for details.", strings.Join(bad, ", "))
}

// Doctor writes the human doctor report.
func Doctor(w io.Writer, r doctor.Report) {
	p := printer{w: w}
	p.line("tidewake doctor · rules %s", r.RulesVersion)
	for _, h := range r.Harnesses {
		p.line("")
		if !h.Installed {
			p.line("%s: not found on PATH", h.Label)
			continue
		}
		p.line("%s %s · %d known leak fix(es) already in your version", h.Label, h.Version, h.Fixed)
		for _, is := range h.Affected {
			p.line("  UPGRADE  %s (fixed in %s)", is.Title, is.FixedIn)
		}
		for _, is := range h.Open {
			covers := ""
			if is.Covers != "" {
				covers = " · tidewake: " + is.Covers
			}
			p.line("  OPEN     %s%s", is.Title, covers)
			p.line("           %s", is.URL)
		}
	}
	p.line("")
	if len(r.MCPDuplicates) == 0 {
		p.line("MCP: no server runs once per session")
	} else {
		p.line("MCP: these servers run once per session")
		for _, g := range r.MCPDuplicates {
			p.line("  %d sessions · %8s  %s", g.Sessions, mem(g.RSSKB), p.truncate(g.Command))
		}
		p.line("  A stateless server can run once behind an MCP gateway (for example mcp-proxy or MetaMCP)")
		p.line("  and be added to each session as an HTTP server. Keep browser and stateful servers per session.")
	}
	p.line("")
	p.line("State sources:")
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p.line("  %-28s %s", n, r.Sources[n])
	}
}

func (p printer) truncate(s string) string {
	s = p.path(s)
	r := []rune(s)
	if len(r) <= commandWidth {
		return s
	}
	return string(r[:commandWidth-1]) + "…"
}

func session(f attrib.Finding) string {
	if f.SessionID == "" {
		return "-"
	}
	id := f.SessionID
	if len(id) > 8 {
		id = id[:8]
	}
	if f.SessionName != "" {
		name := f.SessionName
		if len(name) > 12 {
			name = name[:12]
		}
		return id + " " + name
	}
	return id
}

func countKind(fs []attrib.Finding, k attrib.Kind) int {
	n := 0
	for _, f := range fs {
		if f.Kind == k {
			n++
		}
	}
	return n
}

func age(now, started time.Time) string {
	if started.IsZero() {
		return "?"
	}
	return dur(now.Sub(started))
}

func dur(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func mem(kib int64) string { return size(kib * kb) }

func size(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}

func lastElem(p string) string {
	if i := strings.LastIndex(p, string(os.PathSeparator)); i >= 0 {
		return p[i+1:]
	}
	return p
}
