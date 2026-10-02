//go:build linux

package proc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const procRoot = "/proc"

// subreaperComms name processes that adopt orphans on Linux besides PID 1:
// the per-user systemd manager and common container inits.
var subreaperComms = map[string]bool{"systemd": true, "tini": true, "dumb-init": true, "catatonit": true}

// System collects a snapshot from /proc.
type System struct{}

// Collect reads every process in /proc. Environment, working directory and
// open sockets are read for the current user's processes only.
func (System) Collect(ctx context.Context) (*Snapshot, error) {
	boot, err := bootTime()
	if err != nil {
		return nil, err
	}
	pids, err := listPIDs()
	if err != nil {
		return nil, err
	}
	me := os.Getuid()
	pageKiB := int64(os.Getpagesize() / 1024)
	listeners := readListeners()

	procs := map[int]*Process{}
	for _, pid := range pids {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p, ok := readProcess(pid, boot, pageKiB)
		if !ok {
			continue // exited while we were reading
		}
		if p.UID == me {
			dir := filepath.Join(procRoot, strconv.Itoa(pid))
			p.Env = readEnviron(dir)
			p.Cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
			p.Exe, _ = os.Readlink(filepath.Join(dir, "exe"))
			p.Ports, p.Exposed = socketPorts(dir, listeners)
		}
		procs[pid] = p
	}
	return &Snapshot{Taken: time.Now(), Procs: procs}, nil
}

// SampleCPU reads every process's CPU time from /proc.
func (System) SampleCPU(context.Context) (map[int]time.Duration, error) {
	pids, err := listPIDs()
	if err != nil {
		return nil, err
	}
	out := make(map[int]time.Duration, len(pids))
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		if st, err := ParseProcStat(string(data)); err == nil {
			out[pid] = TicksToDuration(st.CPUTicks)
		}
	}
	return out, nil
}

func bootTime() (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, fmt.Errorf("read /proc/stat: %w", err)
	}
	return ParseBootTime(string(data))
}

func listPIDs() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func readProcess(pid int, boot time.Time, pageKiB int64) (*Process, bool) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	statData, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return nil, false
	}
	st, err := ParseProcStat(string(statData))
	if err != nil {
		return nil, false
	}
	statusData, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return nil, false
	}
	uid, err := ParseStatusUID(string(statusData))
	if err != nil {
		return nil, false
	}
	cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
	command := ParseCmdline(cmdline)
	if command == "" {
		command = "[" + st.Comm + "]" // kernel thread or zombie
	}
	cgroup, _ := os.ReadFile(filepath.Join(dir, "cgroup"))
	return &Process{
		PID: st.PID, PPID: st.PPID, PGID: st.PGID, UID: uid,
		Stat:    linuxStat(st.State),
		RSSKB:   st.RSSPages * pageKiB,
		CPUTime: TicksToDuration(st.CPUTicks),
		// Truncated to seconds, like ps on macOS, so start times compare alike.
		Started: boot.Add(TicksToDuration(st.StartTicks)).Truncate(time.Second),
		Command: command,
		Service: IsServiceCgroup(string(cgroup)),
		Reaper:  pid == InitPID || subreaperComms[st.Comm],
	}, true
}

// linuxStat maps the Linux state letter onto the ps letters tidewake checks:
// Z (zombie) and X (dead) both mean only the parent can clear the process.
func linuxStat(state string) string {
	if state == "X" {
		return "Z"
	}
	return state
}

func readEnviron(dir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(dir, "environ"))
	if err != nil {
		return nil
	}
	return ParseEnviron(data)
}

// readListeners maps socket inode to listener for every TCP listener.
func readListeners() map[uint64]Listener {
	out := map[uint64]Listener{}
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(procRoot, "net", name))
		if err != nil {
			continue
		}
		for _, l := range ParseNetTCP(string(data)) {
			out[l.Inode] = l
		}
	}
	return out
}

// socketPorts returns the listening ports a process holds open.
func socketPorts(dir string, listeners map[uint64]Listener) (ports, exposed []int) {
	if len(listeners) == 0 {
		return nil, nil
	}
	fds, err := os.ReadDir(filepath.Join(dir, "fd"))
	if err != nil {
		return nil, nil
	}
	seen := map[int]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
		if err != nil {
			continue
		}
		inode, ok := SocketInode(link)
		if !ok {
			continue
		}
		l, ok := listeners[inode]
		if !ok || seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		ports = append(ports, l.Port)
		if l.Wildcard {
			exposed = append(exposed, l.Port)
		}
	}
	return ports, exposed
}
