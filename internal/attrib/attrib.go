// Package attrib decides, for every process, whether an agent session left it
// behind, and records the proof for that verdict.
//
// Safety rule: a cleanup command is printed only when the evidence is strong,
// and a process tree is cut at anything that belongs elsewhere.
package attrib

import (
	"fmt"
	"regexp"
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
	// Suspect: orphaned and running from an agent path, or attributed to a
	// session whose liveness could not be read.
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
	Exposed     []int     `json:"exposedPorts,omitempty"`
	Proof       []string  `json:"proof"`
	Suggest     string    `json:"suggest,omitempty"`
	Caution     string    `json:"caution,omitempty"`

	// unsure marks a verdict without readable session state; it never gets a command.
	unsure bool
}

// Input is everything classification needs.
type Input struct {
	Snap  *proc.Snapshot
	State *harness.State
	Pack  *rules.Pack
	UID   int
	// Self is tidewake's own pid; it, its ancestors and its children are never flagged.
	Self int
}

type attribution struct {
	harness, sessionID, via string
}

// Classify returns findings sorted by tree RSS, largest first.
func Classify(in Input) []Finding {
	c := newClassifier(in)
	flagged := map[int]*Finding{}
	for _, p := range in.Snap.Procs {
		if f := c.classify(p); f != nil {
			flagged[p.PID] = f
		}
	}
	findings := c.buildTrees(flagged)
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
	selfLine    map[int]bool // Self and its ancestors
	sessionPath map[string]*regexp.Regexp
	children    map[int][]*proc.Process
}

func newClassifier(in Input) *classifier {
	c := &classifier{in: in, harnessPIDs: map[int]string{}, selfLine: map[int]bool{in.Self: true},
		sessionPath: map[string]*regexp.Regexp{}, children: map[int][]*proc.Process{}}
	for _, p := range in.Snap.Procs {
		c.children[p.PPID] = append(c.children[p.PPID], p)
		for name, h := range in.Pack.Harnesses {
			if h.IsHarnessProcess(p.Name(), p.Command) {
				c.harnessPIDs[p.PID] = name
			}
		}
	}
	for _, a := range in.Snap.Ancestors(in.Self) {
		c.selfLine[a.PID] = true
	}
	for name, h := range in.Pack.Harnesses {
		if h.SessionPathPattern != "" {
			c.sessionPath[name] = regexp.MustCompile(h.SessionPathPattern)
		}
	}
	return c
}

// untouchable processes are never flagged and never put in a kill list.
func (c *classifier) untouchable(p *proc.Process) bool {
	return p.UID != c.in.UID || p.PID == proc.InitPID || p.Reaper || p.Service || c.selfLine[p.PID] || c.isSelfDescendant(p)
}

func (c *classifier) classify(p *proc.Process) *Finding {
	if c.untouchable(p) {
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
	names := c.harnessNames()
	for _, name := range names {
		h := c.in.Pack.Harnesses[name]
		if id := p.Env[h.SessionEnv]; id != "" {
			return &attribution{name, id, fmt.Sprintf("carries %s=%s", h.SessionEnv, short(id))}
		}
	}
	for _, name := range names {
		re := c.sessionPath[name]
		if re == nil {
			continue
		}
		for _, s := range []string{p.Cwd, p.Command} {
			if m := re.FindStringSubmatch(s); m != nil {
				return &attribution{name, m[1], fmt.Sprintf("runs in session %s's scratch directory", short(m[1]))}
			}
		}
	}
	return nil
}

func (c *classifier) harnessNames() []string {
	names := make([]string, 0, len(c.in.Pack.Harnesses))
	for name := range c.in.Pack.Harnesses {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		f.Kind = Suspect
		f.unsure = true
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
	if !c.in.Snap.IsReaper(p.PPID) {
		return nil
	}
	leaderGone := p.PGID != p.PID && !c.in.Snap.Alive(p.PGID)
	if p.PGID != p.PID && !leaderGone {
		return nil
	}
	harnessName, sign, ok := c.agentSign(p)
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
	f.Proof = []string{"parent exited, adopted by " + c.parentName(p), group, sign}
	return f
}

// agentSign finds evidence that an agent harness started p: an agent path,
// or a marker variable such as CODEX_SANDBOX that only harness children carry.
func (c *classifier) agentSign(p *proc.Process) (harness, sign string, ok bool) {
	for _, name := range c.harnessNames() {
		h := c.in.Pack.Harnesses[name]
		if h.InAgentPath(p.Cwd) || h.InAgentPath(p.Command) {
			return name, fmt.Sprintf("runs from a %s path", h.Label), true
		}
		for _, env := range h.MarkerEnv {
			if p.Env[env] != "" {
				return name, fmt.Sprintf("carries %s, set only for %s children", env, h.Label), true
			}
		}
	}
	return "", "", false
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
	if parent := c.in.Snap.Get(p.PPID); parent != nil {
		return parent.Name()
	}
	return strconv.Itoa(p.PPID)
}

// cleanable reports a verdict whose tree may be gathered and stopped.
func cleanable(k Kind) bool { return k == Leftover || k == Suspect }

// grouped reports a verdict shown as one row per process tree.
func grouped(k Kind) bool { return cleanable(k) || k == Detached }

// buildTrees climbs each cleanable finding to its orphaned group root, gathers
// the tree under it (cut at anything that belongs elsewhere), and drops
// findings already covered by another tree.
func (c *classifier) buildTrees(flagged map[int]*Finding) []Finding {
	roots := map[int]*Finding{}
	for _, f := range flagged {
		if !grouped(f.Kind) {
			continue
		}
		root := c.climb(c.in.Snap.Get(f.PID), f)
		if root.PID != f.PID {
			c.moveToRoot(f, root)
		}
		if prev, ok := roots[f.PID]; !ok || f.Kind == Leftover && prev.Kind != Leftover {
			roots[f.PID] = f
		}
	}

	covered := map[int]bool{}
	var out []Finding
	for _, pid := range c.shallowestFirst(roots) {
		f := roots[pid]
		if covered[f.PID] {
			continue
		}
		c.fillTree(f)
		for _, t := range f.TreePIDs {
			covered[t] = true
		}
		out = append(out, *f)
	}
	for _, f := range flagged {
		if grouped(f.Kind) || covered[f.PID] {
			continue
		}
		f.TreePIDs, f.TreeRSSKB = []int{f.PID}, c.in.Snap.Get(f.PID).RSSKB
		f.Suggest, f.Caution = suggestion(f)
		out = append(out, *f)
	}
	return out
}

// moveToRoot rewrites a finding found on a child so the row and its proof
// describe the orphaned group root.
func (c *classifier) moveToRoot(f *Finding, root *proc.Process) {
	child := f.PID
	proof := make([]string, 0, len(f.Proof)+1)
	for _, line := range f.Proof {
		if strings.HasPrefix(line, "no ") && strings.Contains(line, " process above it") {
			continue // recomputed below for the root
		}
		if strings.HasPrefix(line, "parent exited") || strings.HasPrefix(line, "its process group") || strings.HasPrefix(line, "it leads") {
			continue
		}
		if strings.HasPrefix(line, "session ") || strings.HasPrefix(line, "no running") {
			proof = append(proof, line)
			continue
		}
		proof = append(proof, fmt.Sprintf("its child pid %d %s", child, line))
	}
	proof = append(proof, fmt.Sprintf("it is the orphaned root of that process group (parent: %s)", c.parentName(root)))
	f.Proof = proof
	f.PID, f.PPID, f.Command, f.Cwd, f.Started = root.PID, root.PPID, root.Command, root.Cwd, root.Started
}

// climb walks up from p while the parent is an orphaned member of the same
// process group with no conflicting owner. Node and Postgres rewrite their
// process title, which can hide the session id on the real root.
func (c *classifier) climb(p *proc.Process, f *Finding) *proc.Process {
	for {
		parent := c.in.Snap.Get(p.PPID)
		if parent == nil || c.in.Snap.IsReaper(parent.PID) || parent.PGID != p.PGID || !c.belongsWith(parent, f) {
			return p
		}
		p = parent
	}
}

// belongsWith reports whether p may be stopped together with finding f.
func (c *classifier) belongsWith(p *proc.Process, f *Finding) bool {
	if c.untouchable(p) || c.harnessPIDs[p.PID] != "" {
		return false
	}
	a := c.attribute(p)
	if a == nil {
		return true
	}
	if f.SessionID != "" && (a.harness != f.Harness || a.sessionID != f.SessionID) {
		return false
	}
	if f.Kind == Detached {
		return true // same running session: one row, still no command
	}
	sess := c.in.State.Lookup(a.harness, a.sessionID)
	return sess == nil || !sess.Alive
}

func (c *classifier) shallowestFirst(roots map[int]*Finding) []int {
	pids := make([]int, 0, len(roots))
	depth := map[int]int{}
	for pid := range roots {
		pids = append(pids, pid)
		depth[pid] = len(c.in.Snap.Ancestors(pid))
	}
	sort.Slice(pids, func(i, j int) bool {
		if depth[pids[i]] != depth[pids[j]] {
			return depth[pids[i]] < depth[pids[j]]
		}
		return pids[i] < pids[j]
	})
	return pids
}

// fillTree gathers the root and every descendant that belongs with it. A
// descendant that belongs elsewhere is left out together with its subtree.
func (c *classifier) fillTree(f *Finding) {
	excluded := 0
	queue := []*proc.Process{c.in.Snap.Get(f.PID)}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p.PID != f.PID && !c.belongsWith(p, f) {
			excluded += 1 + len(c.in.Snap.Descendants(p.PID))
			continue
		}
		f.TreePIDs = append(f.TreePIDs, p.PID)
		f.TreeRSSKB += p.RSSKB
		f.Ports = append(f.Ports, p.Ports...)
		f.Exposed = append(f.Exposed, p.Exposed...)
		queue = append(queue, c.children[p.PID]...)
	}
	if excluded > 0 {
		f.Proof = append(f.Proof, fmt.Sprintf("%d process(es) under it belong to something still running and are left out", excluded))
	}
	f.Suggest, f.Caution = suggestion(f)
}

// suggestion returns the cleanup command, or none when tidewake cannot be sure.
func suggestion(f *Finding) (cmd, caution string) {
	switch {
	case f.Kind == Detached:
		return "", "its session is still running; stop it from that session if unwanted" + exposedNote(f)
	case f.Kind == Stuck:
		return "", "clears when its parent exits"
	case f.unsure:
		return "", "session state could not be read; no command until liveness is known"
	case isDesktopApp(f.Command):
		return "", "a desktop app, probably opened on purpose; quit it normally if unwanted"
	case len(f.Ports) > 0:
		return "", "serves " + portList(f.Ports) + "; it may be a service you use. If not: kill -TERM " + joinInts(f.TreePIDs) +
			exposedNote(f)
	}
	return "kill -TERM " + joinInts(f.TreePIDs), ""
}

func exposedNote(f *Finding) string {
	if len(f.Exposed) == 0 {
		return ""
	}
	return ". " + portList(f.Exposed) + " listens on all network interfaces, so other machines on your network can reach it"
}

func portList(ports []int) string {
	seen := map[int]bool{}
	var s []string
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			s = append(s, ":"+strconv.Itoa(p))
		}
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
	appMainExecutable  = ".app/Contents/MacOS/"
	headlessFlagPrefix = "--headless"
)

// isDesktopApp reports the main executable of an app in /Applications running
// with a window. Helpers inside the bundle and headless browsers do not count.
func isDesktopApp(command string) bool {
	return strings.HasPrefix(command, appsDir) && strings.Contains(command, appMainExecutable) &&
		!strings.Contains(command, headlessFlagPrefix)
}

func short(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}
