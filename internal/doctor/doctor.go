// Package doctor checks installed harness versions against the known-issue
// table and spots MCP servers duplicated across sessions.
package doctor

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// HarnessReport is one harness's version check.
type HarnessReport struct {
	Name      string        `json:"name"`
	Label     string        `json:"label"`
	Installed bool          `json:"installed"`
	Version   string        `json:"version,omitempty"`
	Affected  []rules.Issue `json:"affected,omitempty"` // fixed upstream in a newer version
	Open      []rules.Issue `json:"open,omitempty"`     // not fixed upstream yet
	Fixed     int           `json:"fixedInYourVersion"`
	Installs  []Install     `json:"installs"`
}

// Install is one copy of a harness binary on this machine.
type Install struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Parsed  string `json:"-"`
	Running bool   `json:"running"`
	OnPath  bool   `json:"onPath"`
}

// MCPGroup is one MCP server command running as several copies.
type MCPGroup struct {
	Command  string `json:"command"`
	Copies   int    `json:"copies"`
	Sessions int    `json:"sessions"`
	RSSKB    int64  `json:"rssKb"`
}

// Report is the full doctor output.
type Report struct {
	RulesVersion  string            `json:"rulesVersion"`
	Harnesses     []HarnessReport   `json:"harnesses"`
	MCPDuplicates []MCPGroup        `json:"mcpDuplicates"`
	Sources       map[string]string `json:"sources"`
}

// Run builds the doctor report.
func Run(ctx context.Context, pack *rules.Pack, run harness.Runner, snap *proc.Snapshot, st *harness.State) Report {
	r := Report{RulesVersion: pack.Version, Sources: map[string]string{}}
	for _, name := range sortedKeys(pack.Harnesses) {
		r.Harnesses = append(r.Harnesses, checkHarness(ctx, name, pack, run, snap))
	}
	r.MCPDuplicates = DuplicateMCP(snap, pack)
	for src, err := range st.Sources {
		r.Sources[src] = "ok"
		if err != nil {
			r.Sources[src] = err.Error()
		}
	}
	return r
}

func checkHarness(ctx context.Context, name string, pack *rules.Pack, run harness.Runner, snap *proc.Snapshot) HarnessReport {
	h := pack.Harnesses[name]
	hr := HarnessReport{Name: name, Label: h.Label}
	hr.Installs = findInstalls(ctx, h, run, snap)
	checked := checkedInstall(hr.Installs)
	if checked == nil {
		return hr
	}
	v, _ := rules.ParseVersion(checked.Version)
	hr.Installed, hr.Version = true, v.String()
	for _, is := range pack.KnownIssues {
		if is.Harness != name {
			continue
		}
		if is.Status == "open" {
			hr.Open = append(hr.Open, is)
			continue
		}
		fixed, _ := rules.ParseVersion(is.FixedIn)
		if v.Less(fixed) {
			hr.Affected = append(hr.Affected, is)
		} else {
			hr.Fixed++
		}
	}
	return hr
}

// findInstalls asks every running harness binary and the one on PATH for its
// version. Several installs are common: a Homebrew CLI next to an app's own copy.
func findInstalls(ctx context.Context, h rules.Harness, run harness.Runner, snap *proc.Snapshot) []Install {
	byPath := map[string]*Install{}
	var order []string
	add := func(path string, running, onPath bool) {
		if path == "" {
			return
		}
		key := path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			key = resolved
		}
		in := byPath[key]
		if in == nil {
			in = &Install{Path: path}
			byPath[key] = in
			order = append(order, key)
		}
		in.Running = in.Running || running
		in.OnPath = in.OnPath || onPath
	}
	for _, p := range snap.Procs {
		if p.Exe != "" && h.IsHarnessProcess(p.Name(), p.Command) && isCLIBinary(h, p.Exe) {
			add(p.Exe, true, false)
		}
	}
	if path, err := lookPath(h.VersionCommand[0]); err == nil {
		add(path, false, true)
	}
	sort.Strings(order)
	var out []Install
	for _, key := range order {
		in := byPath[key]
		args := h.VersionCommand[1:]
		if v, err := run(ctx, in.Path, args...); err == nil {
			if pv, err := rules.ParseVersion(string(v)); err == nil {
				in.Version = strings.TrimSpace(string(v))
				in.Parsed = pv.String()
				out = append(out, *in)
			}
		}
	}
	return out
}

// lookPath finds the binary on PATH; tests replace it.
var lookPath = exec.LookPath

// isCLIBinary reports an executable named like the harness CLI (claude, codex);
// app main executables do not answer --version.
func isCLIBinary(h rules.Harness, exe string) bool {
	base := filepath.Base(exe)
	for _, n := range h.ProcessNames {
		if base == n {
			return true
		}
	}
	return false
}

// checkedInstall picks the install known issues are checked against: the
// oldest running one, since that is what can leak right now, else the one on PATH.
func checkedInstall(ins []Install) *Install {
	var best *Install
	for i := range ins {
		in := &ins[i]
		if !in.Running {
			continue
		}
		if best == nil || older(in, best) {
			best = in
		}
	}
	if best != nil {
		return best
	}
	for i := range ins {
		if ins[i].OnPath {
			return &ins[i]
		}
	}
	return nil
}

func older(a, b *Install) bool {
	va, _ := rules.ParseVersion(a.Parsed)
	vb, _ := rules.ParseVersion(b.Parsed)
	return va.Less(vb)
}

// minDuplicateCopies is how many copies of one server make it worth reporting.
const minDuplicateCopies = 2

// DuplicateMCP groups MCP server processes that sit directly under a harness
// process by command line, and returns commands running as several copies:
// once per Claude session, or once per thread under one Codex app-server.
func DuplicateMCP(snap *proc.Snapshot, pack *rules.Pack) []MCPGroup {
	type acc struct {
		parents map[int]bool
		copies  int
		rss     int64
	}
	groups := map[string]*acc{}
	for _, p := range snap.Procs {
		parent := snap.Get(p.PPID)
		if parent == nil || !isHarness(parent, pack) || !looksLikeMCP(p.Command) {
			continue
		}
		g := groups[p.Command]
		if g == nil {
			g = &acc{parents: map[int]bool{}}
			groups[p.Command] = g
		}
		g.parents[parent.PID] = true
		g.copies++
		g.rss += p.RSSKB
		for _, d := range snap.Descendants(p.PID) {
			g.rss += d.RSSKB
		}
	}
	var out []MCPGroup
	for cmd, g := range groups {
		if g.copies >= minDuplicateCopies {
			out = append(out, MCPGroup{Command: cmd, Copies: g.copies, Sessions: len(g.parents), RSSKB: g.rss})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSSKB > out[j].RSSKB })
	return out
}

func isHarness(p *proc.Process, pack *rules.Pack) bool {
	for _, h := range pack.Harnesses {
		if h.IsHarnessProcess(p.Name(), p.Command) {
			return true
		}
	}
	return false
}

func looksLikeMCP(command string) bool {
	return strings.Contains(strings.ToLower(command), "mcp")
}

func sortedKeys(m map[string]rules.Harness) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
