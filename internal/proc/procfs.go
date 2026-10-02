package proc

// Parsers for Linux /proc files. They have no build tag so their tests run on
// every platform; the collector that uses them is collect_linux.go.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ClockTicks is USER_HZ, the unit of CPU and start times in /proc/<pid>/stat.
// It is 100 on every mainstream Linux architecture; reading it needs cgo.
const ClockTicks = 100

// ProcStat is the subset of /proc/<pid>/stat tidewake uses.
type ProcStat struct {
	PID, PPID, PGID int
	Comm            string
	State           string
	CPUTicks        uint64 // utime + stime
	StartTicks      uint64 // since boot
	RSSPages        int64
}

// Field numbers from proc(5), counted from 1; pid and comm come before ")".
const (
	statState     = 3
	statPPID      = 4
	statPGRP      = 5
	statUtime     = 14
	statStime     = 15
	statStartTime = 22
	statRSS       = 24
)

// ParseProcStat parses /proc/<pid>/stat. The command name sits in parentheses
// and may itself contain spaces and parentheses, so fields are counted from
// the last ")".
func ParseProcStat(s string) (ProcStat, error) {
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return ProcStat{}, fmt.Errorf("stat: no command name in %q", s)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return ProcStat{}, fmt.Errorf("stat pid: %w", err)
	}
	rest := strings.Fields(s[close+1:])
	field := func(n int) string { return rest[n-statState] } // rest[0] is field 3
	if len(rest) < statRSS-statState+1 {
		return ProcStat{}, fmt.Errorf("stat: %d fields after the name", len(rest))
	}
	st := ProcStat{PID: pid, Comm: s[open+1 : close], State: field(statState)}
	ints := []struct {
		n   int
		dst *int
	}{{statPPID, &st.PPID}, {statPGRP, &st.PGID}}
	for _, f := range ints {
		if *f.dst, err = strconv.Atoi(field(f.n)); err != nil {
			return ProcStat{}, fmt.Errorf("stat field %d: %w", f.n, err)
		}
	}
	utime, err1 := strconv.ParseUint(field(statUtime), 10, 64)
	stime, err2 := strconv.ParseUint(field(statStime), 10, 64)
	start, err3 := strconv.ParseUint(field(statStartTime), 10, 64)
	rss, err4 := strconv.ParseInt(field(statRSS), 10, 64)
	for _, e := range []error{err1, err2, err3, err4} {
		if e != nil {
			return ProcStat{}, fmt.Errorf("stat: %w", e)
		}
	}
	st.CPUTicks, st.StartTicks, st.RSSPages = utime+stime, start, rss
	return st, nil
}

// TicksToDuration converts clock ticks to a duration.
func TicksToDuration(t uint64) time.Duration {
	return time.Duration(t) * time.Second / ClockTicks
}

// ParseStatusUID returns the real uid from /proc/<pid>/status.
func ParseStatusUID(s string) (int, error) {
	for _, line := range strings.Split(s, "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				break
			}
			return strconv.Atoi(f[0])
		}
	}
	return 0, fmt.Errorf("status: no Uid line")
}

// ParseBootTime returns the btime line of /proc/stat.
func ParseBootTime(s string) (time.Time, error) {
	for _, line := range strings.Split(s, "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(n, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("/proc/stat: no btime")
}

// ParseCmdline turns NUL-separated /proc/<pid>/cmdline into one line.
func ParseCmdline(b []byte) string {
	return strings.TrimSpace(string(bytes.ReplaceAll(bytes.TrimRight(b, "\x00"), []byte{0}, []byte{' '})))
}

// ParseEnviron keeps only allow-listed variables from /proc/<pid>/environ.
func ParseEnviron(b []byte) map[string]string {
	env := map[string]string{}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if k, v, ok := strings.Cut(string(kv), "="); ok && AttributionEnv[k] {
			env[k] = v
		}
	}
	return env
}

// IsServiceCgroup reports a process run by systemd as a service (the last
// path element of its cgroup is a .service unit). Like a launchd job, its
// manager owns its lifecycle.
func IsServiceCgroup(s string) bool {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimRight(parts[2], "/")
		if strings.HasSuffix(path[strings.LastIndexByte(path, '/')+1:], ".service") {
			return true
		}
	}
	return false
}

// Listener is one listening TCP socket from /proc/net/tcp or tcp6.
type Listener struct {
	Inode    uint64
	Port     int
	Wildcard bool // bound to every interface
}

// tcpListen is the state code for LISTEN in /proc/net/tcp.
const tcpListen = "0A"

// ParseNetTCP parses /proc/net/tcp or /proc/net/tcp6 into listening sockets.
func ParseNetTCP(s string) []Listener {
	var out []Listener
	for i, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 10 || f[3] != tcpListen {
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err1 := strconv.ParseUint(portHex, 16, 32)
		inode, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Listener{Inode: inode, Port: int(port), Wildcard: strings.Trim(addr, "0") == ""})
	}
	return out
}

// SocketInode extracts the inode from an fd link such as "socket:[12345]".
func SocketInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSuffix(rest, "]"), 10, 64)
	return n, err == nil
}
