// Package health reports on live Claude Code and Codex sessions: memory, CPU,
// and whether a session looks stuck. It reads state only and stops nothing.
package health

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// Thresholds decide when a row gets a flag.
type Thresholds struct {
	Stuck time.Duration // busy this long with no activity
	Idle  time.Duration // idle this long while holding memory
	RAMKB int64         // tree memory worth flagging
}

// DefaultThresholds are deliberately in hours: a healthy busy session can go
// half an hour without writing to its transcript.
var DefaultThresholds = Thresholds{Stuck: 2 * time.Hour, Idle: 24 * time.Hour, RAMKB: KiBFromBytes(4e9)}

const (
	// bytesPerKiB: ps reports RSS in KiB.
	bytesPerKiB = 1024
	// stuckMaxCPU: a session using more CPU than this over the sample is working.
	stuckMaxCPU = 1.0
	// idleHoldKB: an idle session holding less than this (1 GB) is not worth a flag.
	idleHoldKB = int64(1e9) / bytesPerKiB
	// busyIdleCPU: an idle session using this much CPU is spinning.
	busyIdleCPU = 50.0
	// idleSettle: ignore CPU right after a session went idle; it may be finishing up.
	idleSettle = 5 * time.Minute
)

// Flag kinds.
const (
	FlagStuck     = "looks-stuck"
	FlagHighRAM   = "high-ram"
	FlagIdleHold  = "idle-holding"
	FlagSpinning  = "cpu-while-idle"
	statusBusy    = "busy"
	statusIdle    = "idle"
	claudeHarness = "claude"
	codexHarness  = "codex"
)

// Flag is one finding about a session, with its proof.
type Flag struct {
	Kind   string   `json:"kind"`
	Text   string   `json:"text"`
	Proof  []string `json:"proof"`
	Advice string   `json:"advice,omitempty"`
}

// Child is a direct child process tree, for naming what holds memory.
type Child struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	RSSKB   int64  `json:"rssKb"`
}

// Row is one live session or harness process.
type Row struct {
	Harness      string    `json:"harness"`
	SessionID    string    `json:"sessionId,omitempty"`
	Name         string    `json:"name,omitempty"`
	Cwd          string    `json:"cwd,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	Status       string    `json:"status,omitempty"`
	StatusSince  time.Time `json:"statusSince,omitempty"`
	PID          int       `json:"pid"`
	Command      string    `json:"command"`
	Started      time.Time `json:"started"`
	TreeRSSKB    int64     `json:"treeRssKb"`
	CPUPercent   float64   `json:"cpuPercent"`
	LastActivity time.Time `json:"lastActivity,omitempty"`
	Biggest      *Child    `json:"biggestChild,omitempty"`
	Flags        []Flag    `json:"flags"`
}

// ClaudeSession is the subset of ~/.claude/sessions/<pid>.json health needs.
type ClaudeSession struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Name            string `json:"name"`
	Cwd             string `json:"cwd"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	ProcStart       string `json:"procStart"`
}

// Input is everything a health report needs.
type Input struct {
	Snap     *proc.Snapshot
	CPUAfter map[int]time.Duration // second CPU sample, Interval after Snap
	Interval time.Duration
	Sessions []ClaudeSession
	// Activity returns when a Claude session last wrote its transcript.
	Activity func(sessionID string) time.Time
	Pack     *rules.Pack
	Limits   Thresholds
	Now      time.Time
}

// ReadClaudeSessions reads every session file; unreadable ones are skipped.
func ReadClaudeSessions(dir string) ([]ClaudeSession, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []ClaudeSession
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s ClaudeSession
		if json.Unmarshal(data, &s) == nil && s.SessionID != "" && s.PID > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// TranscriptActivity returns a function giving the newest write time of a
// session's transcript and its subagent transcripts. Only file times are read.
func TranscriptActivity(projectsDir string) func(string) time.Time {
	return func(sessionID string) time.Time {
		var newest time.Time
		note := func(path string) {
			if info, err := os.Stat(path); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		mains, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
		for _, m := range mains {
			note(m)
		}
		dirs, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID))
		for _, d := range dirs {
			_ = filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
				if err == nil && !e.IsDir() && strings.HasSuffix(path, ".jsonl") {
					note(path)
				}
				return nil
			})
		}
		return newest
	}
}

// Report builds one row per live Claude session and per top-level Codex
// process, sorted by memory.
func Report(in Input) []Row {
	children := childIndex(in.Snap)
	var rows []Row
	for _, s := range in.Sessions {
		started, _ := time.ParseInLocation(proc.LstartLayout, s.ProcStart, time.UTC)
		p := in.Snap.Get(s.PID)
		if p == nil || !in.Snap.AliveSince(s.PID, started) {
			continue
		}
		rows = append(rows, claudeRow(in, s, p, children))
	}
	rows = append(rows, codexRows(in, children)...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].TreeRSSKB > rows[j].TreeRSSKB })
	return rows
}

func claudeRow(in Input, s ClaudeSession, p *proc.Process, children map[int][]*proc.Process) Row {
	r := baseRow(in, p, children)
	r.Harness, r.SessionID, r.Name, r.Cwd, r.Kind, r.Status = claudeHarness, s.SessionID, s.Name, s.Cwd, s.Kind, s.Status
	if s.StatusUpdatedAt > 0 {
		r.StatusSince = time.UnixMilli(s.StatusUpdatedAt)
	}
	if in.Activity != nil {
		r.LastActivity = in.Activity(s.SessionID)
	}
	r.Flags = append(r.Flags, stuckFlag(in, r)...)
	r.Flags = append(r.Flags, idleHoldFlag(in, r)...)
	r.Flags = append(r.Flags, spinningFlag(in, r)...)
	r.Flags = append(r.Flags, ramFlag(in, r)...)
	return r
}

// codexRows lists Codex processes that have no Codex process above them:
// the app, an app-server daemon, or a CLI session.
func codexRows(in Input, children map[int][]*proc.Process) []Row {
	h, ok := in.Pack.Harnesses[codexHarness]
	if !ok {
		return nil
	}
	isCodex := func(p *proc.Process) bool { return h.IsHarnessProcess(p.Name(), p.Command) && !p.Exiting() }
	var rows []Row
	for _, p := range in.Snap.Procs {
		if !isCodex(p) || hasAncestor(in.Snap, p, isCodex) {
			continue
		}
		r := baseRow(in, p, children)
		r.Harness, r.Cwd = codexHarness, p.Cwd
		r.Flags = ramFlag(in, r)
		rows = append(rows, r)
	}
	return rows
}

func hasAncestor(snap *proc.Snapshot, p *proc.Process, match func(*proc.Process) bool) bool {
	for _, a := range snap.Ancestors(p.PID) {
		if match(a) {
			return true
		}
	}
	return false
}

func baseRow(in Input, p *proc.Process, children map[int][]*proc.Process) Row {
	tree := append([]*proc.Process{p}, in.Snap.Descendants(p.PID)...)
	r := Row{PID: p.PID, Command: p.Command, Started: p.Started}
	var cpuDelta time.Duration
	for _, t := range tree {
		r.TreeRSSKB += t.RSSKB
		if after, ok := in.CPUAfter[t.PID]; ok && after >= t.CPUTime {
			cpuDelta += after - t.CPUTime
		}
	}
	if in.Interval > 0 {
		r.CPUPercent = 100 * float64(cpuDelta) / float64(in.Interval)
	}
	r.Biggest = biggestChild(in.Snap, children[p.PID])
	return r
}

func biggestChild(snap *proc.Snapshot, kids []*proc.Process) *Child {
	var best *Child
	for _, k := range kids {
		rss := k.RSSKB
		for _, d := range snap.Descendants(k.PID) {
			rss += d.RSSKB
		}
		if best == nil || rss > best.RSSKB {
			best = &Child{PID: k.PID, Command: k.Command, RSSKB: rss}
		}
	}
	return best
}

func childIndex(snap *proc.Snapshot) map[int][]*proc.Process {
	idx := map[int][]*proc.Process{}
	for _, p := range snap.Procs {
		idx[p.PPID] = append(idx[p.PPID], p)
	}
	return idx
}

// stuckFlag needs all three: busy for the threshold, no transcript write for
// the threshold, and almost no CPU during the sample. Any one alone is normal.
func stuckFlag(in Input, r Row) []Flag {
	if r.Status != statusBusy || r.StatusSince.IsZero() || r.LastActivity.IsZero() {
		return nil
	}
	busyFor := in.Now.Sub(r.StatusSince)
	quietFor := in.Now.Sub(r.LastActivity)
	if busyFor < in.Limits.Stuck || quietFor < in.Limits.Stuck || r.CPUPercent >= stuckMaxCPU {
		return nil
	}
	return []Flag{{
		Kind: FlagStuck,
		Text: fmt.Sprintf("looks stuck: busy for %s with no activity", human(busyFor)),
		Proof: []string{
			fmt.Sprintf("status %q since %s", r.Status, r.StatusSince.Local().Format("Jan 2 15:04")),
			fmt.Sprintf("no transcript write for %s", human(quietFor)),
			fmt.Sprintf("%.1f%% CPU over %s", r.CPUPercent, in.Interval.Round(time.Second)),
		},
		Advice: "check its terminal; Esc stops the current turn",
	}}
}

func idleHoldFlag(in Input, r Row) []Flag {
	if r.Status != statusIdle || r.LastActivity.IsZero() || r.TreeRSSKB < idleHoldKB {
		return nil
	}
	quietFor := in.Now.Sub(r.LastActivity)
	if quietFor < in.Limits.Idle {
		return nil
	}
	return []Flag{{
		Kind:   FlagIdleHold,
		Text:   fmt.Sprintf("idle for %s, holding %s", human(quietFor), memText(r.TreeRSSKB)),
		Proof:  []string{fmt.Sprintf("status %q", r.Status), fmt.Sprintf("no transcript write for %s", human(quietFor))},
		Advice: "close it if you are done; /exit also stops its MCP servers",
	}}
}

func spinningFlag(in Input, r Row) []Flag {
	if r.Status != statusIdle || r.CPUPercent < busyIdleCPU || r.StatusSince.IsZero() || in.Now.Sub(r.StatusSince) < idleSettle {
		return nil
	}
	return []Flag{{
		Kind:   FlagSpinning,
		Text:   fmt.Sprintf("using %.0f%% CPU while idle", r.CPUPercent),
		Proof:  []string{fmt.Sprintf("status %q since %s", r.Status, r.StatusSince.Local().Format("Jan 2 15:04")), fmt.Sprintf("%.0f%% CPU over %s", r.CPUPercent, in.Interval.Round(time.Second))},
		Advice: "a known Claude Code issue (claude-code#19393); restarting the session usually stops it",
	}}
}

func ramFlag(in Input, r Row) []Flag {
	if r.TreeRSSKB < in.Limits.RAMKB {
		return nil
	}
	proof := []string{fmt.Sprintf("process tree uses %s", memText(r.TreeRSSKB))}
	if r.Biggest != nil {
		proof = append(proof, fmt.Sprintf("largest part: pid %d using %s", r.Biggest.PID, memText(r.Biggest.RSSKB)))
	}
	return []Flag{{Kind: FlagHighRAM, Text: "high memory: " + memText(r.TreeRSSKB), Proof: proof}}
}

// memText prints ps's KiB as decimal MB/GB, matching the scan report.
func memText(kib int64) string {
	bytes := float64(kib) * bytesPerKiB
	if bytes >= 1e9 {
		return fmt.Sprintf("%.1f GB", bytes/1e9)
	}
	return fmt.Sprintf("%.0f MB", bytes/1e6)
}

// KiBFromBytes converts a byte threshold to ps's KiB unit.
func KiBFromBytes(b int64) int64 { return b / bytesPerKiB }

func human(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
