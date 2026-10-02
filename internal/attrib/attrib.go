// Package attrib decides, for every process, whether an agent session left it
// behind, and records the proof for that verdict.
package attrib

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// Kind is the verdict for a process tree.
type Kind string

const (
	// Leftover: attributed to a session that has ended. Strongest evidence.
	Leftover Kind = "leftover"
	// Suspect: no session id, but orphaned, its group leader gone, and running from an agent path.
	Suspect Kind = "suspect"
	// Detached: attributed to a session that is still running, but no longer under it.
	Detached Kind = "detached"
	// Stuck: exiting or zombie; only its parent can clear it.
	Stuck Kind = "stuck"
)

// Finding is one flagged process tree.
type Finding struct {
	Kind        Kind      `json:"kind"`
	Harness     string    `json:"harness,omitempty"`
	SessionID   string    `json:"sessionId,omitempty"`
	SessionName string    `json:"sessionName,omitempty"`
	PID         int       `json:"pid"`
	PPID        int       `json:"ppid"`
	Command     string    `json:"command"`
	Cwd         string    `json:"cwd,omitempty"`
	Started     time.Time `json:"started"`
	TreePIDs    []int     `json:"treePids"`
	TreeRSSKB   int64     `json:"treeRssKb"`
	Ports       []int     `json:"ports,omitempty"`
	Proof       []string  `json:"proof"`
	Suggest     string    `json:"suggest,omitempty"`
	Caution     string    `json:"caution,omitempty"`
}

// Input is everything classification needs.
type Input struct {
	Snap  *proc.Snapshot
	State *harness.State
	Pack  *rules.Pack
	UID   int
	// Self is tidewake's own pid; it and its children are never flagged.
	Self int
}

type attribution struct {
	harness, sessionID, via string
}

// Classify returns findings sorted by tree RSS, largest first.
func Classify(in Input) []Finding {
	c := classifier{in: in, harnessPIDs: map[int]string{}}
	c.indexHarnessProcesses()

	flagged := map[int]*Finding{}
	for _, p := range in.Snap.Procs {
		if f := c.classify(p); f != nil {
			flagged[p.PID] = f
		}
	}
	findings := c.collapseTrees(flagged)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].TreeRSSKB != findings[j].TreeRSSKB {
			return findings[i].TreeRSSKB > findings[j].TreeRSSKB
		}
		return findings[i].PID < findings[j].PID
	})
	return findings
}

type classifier struct {
	in          Input
	harnessPIDs map[int]string
}

func (c *classifier) indexHarnessProcesses() {
	for _, p := range c.in.Snap.Procs {
		for name, h := range c.in.Pack.Harnesses {
			if h.IsHarnessProcess(p.Name(), p.Command) {
				c.harnessPIDs[p.PID] = name
			}
		}
	}
}

func (c *classifier) classify(p *proc.Process) *Finding {
	if p.UID != c.in.UID || p.PID == c.in.Self || p.PID == proc.LaunchdPID || p.LaunchdJob || c.isSelfDescendant(p) {
		return nil
	}
	_, isHarness := c.harnessPIDs[p.PID]
	attr := c.attribute(p)
	if p.Exiting() && (isHarness || attr != nil) {
		return c.stuck(p, attr)
	}
	if isHarness || c.liveHarnessAncestor(p) != nil {
		return nil
	}
	if attr != nil {
		return c.attributed(p, *attr)
	}
	return c.suspect(p)
}

func (c *classifier) isSelfDescendant(p *proc.Process) bool {
	for _, a := range c.in.Snap.Ancestors(p.PID) {
		if a.PID == c.in.Self {
			return true
		}
	}
	return false
}

func (c *classifier) liveHarnessAncestor(p *proc.Process) *proc.Process {
	for _, a := range c.in.Snap.Ancestors(p.PID) {
		if _, ok := c.harnessPIDs[a.PID]; ok && !a.Exiting() {
			return a
		}
	}
	return nil
}

func (c *classifier) attribute(p *proc.Process) *attribution {
	if conv, ok := c.in.State.CodexProcesses[p.PID]; ok && conv != "" {
		return &attribution{"codex", conv, "listed in Codex chat_processes.json"}
	}
	names := make([]string, 0, len(c.in.Pack.Harnesses))
	for name := range c.in.Pack.Harnesses {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := c.in.Pack.Harnesses[name]
		if id := p.Env[h.SessionEnv]; id != "" {
			return &attribution{name, id, fmt.Sprintf("carries %s=%s", h.SessionEnv, short(id))}
		}
	}
	return nil
}

func (c *classifier) attributed(p *proc.Process, a attribution) *Finding {
	label := c.in.Pack.Harnesses[a.harness].Label
	noParent := fmt.Sprintf("no %s process above it (parent: %s)", label, c.parentName(p))
	f := c.base(p)
	f.Harness, f.SessionID = a.harness, a.sessionID

	sess := c.in.State.Lookup(a.harness, a.sessionID)
	switch {
	case sess != nil && sess.Alive:
		f.Kind = Detached
		f.SessionName = sess.Name
		f.Proof = []string{a.via, "session is still running: " + sess.Why, noParent}
	case sess != nil:
		f.Kind = Leftover
		f.SessionName = sess.Name
		f.Proof = []string{a.via, "session ended: " + sess.Why, noParent}
	case c.harnessSourcesReadable(a.harness):
		f.Kind = Leftover
		f.Proof = []string{a.via, fmt.Sprintf("no running %s session has this id", label), noParent}
	default:
		// Without readable session state an unknown id proves nothing.
		f.Kind = Suspect
		f.Proof = []string{a.via, fmt.Sprintf("%s session state unreadable, liveness unknown", label), noParent}
	}
	return f
}

// harnessSourcesReadable reports whether every state source for a harness was read.
func (c *classifier) harnessSourcesReadable(name string) bool {
	found := false
	for src, err := range c.in.State.Sources {
		if strings.HasPrefix(src, name+" ") {
			found = true
			if err != nil {
				return false
			}
		}
	}
	return found
}

func (c *classifier) suspect(p *proc.Process) *Finding {
	if p.PPID != proc.LaunchdPID {
		return nil
	}
	leaderGone := p.PGID != p.PID && !c.in.Snap.Alive(p.PGID)
	if p.PGID != p.PID && !leaderGone {
		return nil
	}
	harnessName, ok := c.agentPathHarness(p)
	if !ok {
		return nil
	}
	f := c.base(p)
	f.Kind = Suspect
	f.Harness = harnessName
	group := fmt.Sprintf("its process group leader %d has exited", p.PGID)
	if !leaderGone {
		group = "it leads its own process group"
	}
	f.Proof = []string{"parent exited, adopted by launchd", group,
		fmt.Sprintf("runs from a %s path", c.in.Pack.Harnesses[harnessName].Label)}
	return f
}

func (c *classifier) agentPathHarness(p *proc.Process) (string, bool) {
	for name, h := range c.in.Pack.Harnesses {
		if h.InAgentPath(p.Cwd) || h.InAgentPath(p.Command) {
			return name, true
		}
	}
	return "", false
}

func (c *classifier) stuck(p *proc.Process, a *attribution) *Finding {
	f := c.base(p)
	f.Kind = Stuck
	if a != nil {
		f.Harness, f.SessionID = a.harness, a.sessionID
	} else {
		f.Harness = c.harnessPIDs[p.PID]
	}
	f.Proof = []string{fmt.Sprintf("process state %q (exiting or zombie)", p.Stat),
		fmt.Sprintf("only its parent %d (%s) can clear it", p.PPID, c.parentName(p))}
	return f
}

func (c *classifier) base(p *proc.Process) *Finding {
	return &Finding{PID: p.PID, PPID: p.PPID, Command: p.Command, Cwd: p.Cwd, Started: p.Started}
}

func (c *classifier) parentName(p *proc.Process) string {
	if p.PPID == proc.LaunchdPID {
		return "launchd"
	}
	if parent := c.in.Snap.Get(p.PPID); parent != nil {
		return parent.Name()
	}
	return strconv.Itoa(p.PPID)
}

// collapseTrees keeps only the top-most flagged process of each tree and
// folds its descendants into it, so one leftover MCP server and its browser
// read as one row.
func (c *classifier) collapseTrees(flagged map[int]*Finding) []Finding {
	var out []Finding
	for pid, f := range flagged {
		if f.Kind != Stuck && c.hasFlaggedAncestor(pid, flagged) {
			continue
		}
		c.fillTree(f)
		out = append(out, *f)
	}
	return out
}

func (c *classifier) hasFlaggedAncestor(pid int, flagged map[int]*Finding) bool {
	for _, a := range c.in.Snap.Ancestors(pid) {
		if g, ok := flagged[a.PID]; ok && g.Kind != Stuck {
			return true
		}
	}
	return false
}

func (c *classifier) fillTree(f *Finding) {
	root := c.in.Snap.Get(f.PID)
	tree := []*proc.Process{root}
	if f.Kind != Stuck {
		tree = append(tree, c.in.Snap.Descendants(f.PID)...)
	}
	for _, p := range tree {
		f.TreePIDs = append(f.TreePIDs, p.PID)
		f.TreeRSSKB += p.RSSKB
		f.Ports = append(f.Ports, p.Ports...)
	}
	f.Suggest, f.Caution = suggestion(f)
}

func suggestion(f *Finding) (cmd, caution string) {
	if len(f.Ports) > 0 {
		caution = "listens on " + portList(f.Ports) + "; check nothing still uses it"
	}
	if isDesktopApp(f.Command) {
		caution = "a desktop app, probably opened on purpose"
	}
	switch f.Kind {
	case Leftover, Suspect:
		return "kill -TERM " + joinInts(f.TreePIDs), caution
	case Detached:
		return "", "its session is still running; stop it from that session if unwanted"
	default:
		return "", "clears when its parent exits"
	}
}

func portList(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = ":" + strconv.Itoa(p)
	}
	return strings.Join(s, " ")
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, " ")
}

const (
	shortIDLen         = 8
	appsDir            = "/Applications/"
	headlessFlagPrefix = "--headless"
)

// isDesktopApp reports an app from /Applications running with a window;
// a headless browser an agent launched does not count.
func isDesktopApp(command string) bool {
	return strings.HasPrefix(command, appsDir) && !strings.Contains(command, headlessFlagPrefix)
}

func short(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}
