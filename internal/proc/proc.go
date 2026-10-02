// Package proc takes a point-in-time snapshot of the user's processes.
package proc

import (
	"context"
	"path/filepath"
	"time"
)

// LaunchdPID is the PID that adopts processes whose parent exited.
const LaunchdPID = 1

// Process is one process as seen at snapshot time.
type Process struct {
	PID     int
	PPID    int
	PGID    int
	UID     int
	Stat    string
	RSSKB   int64
	Started time.Time
	Command string
	// Env holds only allow-listed attribution variables, never secrets.
	Env   map[string]string
	Cwd   string
	Ports []int
	// LaunchdJob marks a process launchd runs as a service; launchd owns its lifecycle.
	LaunchdJob bool
}

// Name is the base name of the executable.
func (p *Process) Name() string {
	exe, _, _ := cutArgv0(p.Command)
	return filepath.Base(exe)
}

// Exiting reports a process stuck while exiting or a zombie.
func (p *Process) Exiting() bool {
	for _, c := range p.Stat {
		if c == 'E' || c == 'Z' {
			return true
		}
	}
	return false
}

// Snapshot is every visible process at one moment.
type Snapshot struct {
	Taken time.Time
	Procs map[int]*Process
}

// Get returns the process or nil.
func (s *Snapshot) Get(pid int) *Process { return s.Procs[pid] }

// Alive reports whether pid exists in the snapshot.
func (s *Snapshot) Alive(pid int) bool { return s.Procs[pid] != nil }

// AliveSince reports whether pid exists and started at started.
// It guards against PID reuse when a session file outlives its process.
func (s *Snapshot) AliveSince(pid int, started time.Time) bool {
	p := s.Procs[pid]
	if p == nil {
		return false
	}
	if started.IsZero() || p.Started.IsZero() {
		return true
	}
	return p.Started.Equal(started)
}

// Ancestors returns the parent chain of pid, nearest first, excluding launchd.
func (s *Snapshot) Ancestors(pid int) []*Process {
	var chain []*Process
	seen := map[int]bool{pid: true}
	for p := s.Procs[pid]; p != nil; {
		parent := s.Procs[p.PPID]
		if parent == nil || parent.PID == LaunchdPID || seen[parent.PID] {
			break
		}
		seen[parent.PID] = true
		chain = append(chain, parent)
		p = parent
	}
	return chain
}

// Descendants returns every process below pid.
func (s *Snapshot) Descendants(pid int) []*Process {
	children := map[int][]*Process{}
	for _, p := range s.Procs {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []*Process
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, c := range children[next] {
			if seen[c.PID] {
				continue
			}
			seen[c.PID] = true
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// Collector produces snapshots; tests supply fakes.
type Collector interface {
	Collect(ctx context.Context) (*Snapshot, error)
}

func cutArgv0(command string) (exe, rest string, ok bool) {
	for i, c := range command {
		if c == ' ' {
			return command[:i], command[i+1:], true
		}
	}
	return command, "", false
}
