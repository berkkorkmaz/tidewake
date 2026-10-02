// Package proc takes a point-in-time snapshot of the user's processes.
package proc

import (
	"context"
	"path/filepath"
	"time"
)

// InitPID is launchd on macOS and init/systemd on Linux; it adopts orphans.
const InitPID = 1

// Process is one process as seen at snapshot time.
type Process struct {
	PID   int
	PPID  int
	PGID  int
	UID   int
	Stat  string
	RSSKB int64
	// CPUTime is total CPU time used since the process started.
	CPUTime time.Duration
	Started time.Time
	Command string
	// Exe is the executable path the kernel recorded at exec time.
	Exe string
	// Env holds only allow-listed attribution variables, never secrets.
	Env   map[string]string
	Cwd   string
	Ports []int
	// Exposed lists ports bound to every network interface, not just loopback.
	Exposed []int
	// Service marks a launchd job or systemd service; its manager owns its lifecycle.
	Service bool
	// Reaper marks a process that adopts orphans: PID 1, or a Linux subreaper
	// such as `systemd --user` or a container init.
	Reaper bool
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

// StartTolerance absorbs ps's one-second start-time resolution.
const StartTolerance = 2 * time.Second

// AliveSince reports whether pid exists and started at started (within
// StartTolerance). It guards against PID reuse when a state file outlives its
// process. A zero started time cannot be checked and counts as alive.
func (s *Snapshot) AliveSince(pid int, started time.Time) bool {
	p := s.Procs[pid]
	if p == nil {
		return false
	}
	if started.IsZero() || p.Started.IsZero() {
		return true
	}
	d := p.Started.Sub(started)
	return d <= StartTolerance && d >= -StartTolerance
}

// IsReaper reports whether pid adopts orphans.
func (s *Snapshot) IsReaper(pid int) bool {
	if pid == InitPID {
		return true
	}
	p := s.Procs[pid]
	return p != nil && p.Reaper
}

// Ancestors returns the parent chain of pid, nearest first, excluding PID 1.
func (s *Snapshot) Ancestors(pid int) []*Process {
	var chain []*Process
	seen := map[int]bool{pid: true}
	for p := s.Procs[pid]; p != nil; {
		parent := s.Procs[p.PPID]
		if parent == nil || parent.PID == InitPID || seen[parent.PID] {
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

// CPUSampler reads every process's CPU time cheaply, for a second sample.
type CPUSampler interface {
	SampleCPU(ctx context.Context) (map[int]time.Duration, error)
}

func cutArgv0(command string) (exe, rest string, ok bool) {
	for i, c := range command {
		if c == ' ' {
			return command[:i], command[i+1:], true
		}
	}
	return command, "", false
}
