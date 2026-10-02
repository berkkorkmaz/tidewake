// Package doctor checks installed harness versions against the known-issue
// table and spots MCP servers duplicated across sessions.
package doctor

import (
	"context"
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
}

// MCPGroup is one MCP server command running once per session.
type MCPGroup struct {
	Command  string `json:"command"`
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
		r.Harnesses = append(r.Harnesses, checkHarness(ctx, name, pack, run))
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

func checkHarness(ctx context.Context, name string, pack *rules.Pack, run harness.Runner) HarnessReport {
	h := pack.Harnesses[name]
	hr := HarnessReport{Name: name, Label: h.Label}
	out, err := run(ctx, h.VersionCommand[0], h.VersionCommand[1:]...)
	if err != nil {
		return hr
	}
	v, err := rules.ParseVersion(string(out))
	if err != nil {
		return hr
	}
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

// minDuplicateSessions is how many sessions must run the same server to report it.
const minDuplicateSessions = 2

// DuplicateMCP groups MCP server processes that sit directly under a harness
// process by command line, and returns commands run by several sessions.
func DuplicateMCP(snap *proc.Snapshot, pack *rules.Pack) []MCPGroup {
	type acc struct {
		parents map[int]bool
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
		g.rss += p.RSSKB
		for _, d := range snap.Descendants(p.PID) {
			g.rss += d.RSSKB
		}
	}
	var out []MCPGroup
	for cmd, g := range groups {
		if len(g.parents) >= minDuplicateSessions {
			out = append(out, MCPGroup{Command: cmd, Sessions: len(g.parents), RSSKB: g.rss})
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
