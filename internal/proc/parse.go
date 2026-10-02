package proc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PSArgs is the ps invocation whose output ParsePS reads.
var PSArgs = []string{"-axww", "-o", "pid=,ppid=,pgid=,uid=,stat=,rss=,lstart=,command="}

// LstartLayout is the format of ps lstart and of Claude Code's procStart.
const LstartLayout = "Mon Jan _2 15:04:05 2006"

const (
	psFixedFields  = 6 // pid ppid pgid uid stat rss
	lstartFields   = 5 // e.g. "Fri Oct  2 15:12:34 2026"
	psMinFieldsRow = psFixedFields + lstartFields + 1
)

// AttributionEnv lists the only environment variables tidewake keeps.
// Tokens and socket paths that harnesses also export are dropped on purpose.
var AttributionEnv = map[string]bool{
	"CLAUDECODE":                true,
	"CLAUDE_CODE_SESSION_ID":    true,
	"CLAUDE_CODE_ENTRYPOINT":    true,
	"CLAUDE_PROJECT_DIR":        true,
	"CLAUDE_JOB_DIR":            true,
	"CLAUDE_CODE_CHILD_SESSION": true,
	"CODEX_THREAD_ID":           true,
	"CODEX_SESSION_ID":          true,
}

// ParsePS parses the output of `ps` run with PSArgs.
func ParsePS(out []byte, loc *time.Location) (map[int]*Process, error) {
	procs := map[int]*Process{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		p, err := parsePSLine(line, loc)
		if err != nil {
			return nil, err
		}
		procs[p.PID] = p
	}
	return procs, sc.Err()
}

func parsePSLine(line string, loc *time.Location) (*Process, error) {
	f := strings.Fields(line)
	if len(f) < psMinFieldsRow {
		return nil, fmt.Errorf("ps line has %d fields: %q", len(f), line)
	}
	ints := make([]int, 4)
	for i := range ints {
		v, err := strconv.Atoi(f[i])
		if err != nil {
			return nil, fmt.Errorf("ps field %d in %q: %w", i, line, err)
		}
		ints[i] = v
	}
	rss, err := strconv.ParseInt(f[5], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ps rss in %q: %w", line, err)
	}
	started, err := time.ParseInLocation(LstartLayout, strings.Join(f[6:6+lstartFields], " "), loc)
	if err != nil {
		return nil, fmt.Errorf("ps lstart in %q: %w", line, err)
	}
	return &Process{
		PID: ints[0], PPID: ints[1], PGID: ints[2], UID: ints[3],
		Stat: f[4], RSSKB: rss, Started: started,
		Command: commandAfterFields(line, psFixedFields+lstartFields),
	}, nil
}

// commandAfterFields returns the rest of line after n whitespace-separated
// fields, keeping the command's own spacing intact.
func commandAfterFields(line string, n int) string {
	rest := line
	for i := 0; i < n; i++ {
		rest = strings.TrimLeft(rest, " \t")
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			return ""
		}
		rest = rest[idx:]
	}
	return strings.TrimLeft(rest, " \t")
}

// ParseLsofPathByPID parses `lsof -F pn` output into pid -> last name.
// Used with `-d cwd` to get each process's working directory.
func ParseLsofPathByPID(out []byte) map[int]string {
	res := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 {
				res[pid] = line[1:]
			}
		}
	}
	return res
}

// ParseLsofPorts parses `lsof -F pn -iTCP -sTCP:LISTEN` into pid -> ports.
func ParseLsofPorts(out []byte) map[int][]int {
	res := map[int][]int{}
	seen := map[[2]int]bool{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			idx := strings.LastIndex(line, ":")
			if idx < 0 || pid == 0 {
				continue
			}
			port, err := strconv.Atoi(line[idx+1:])
			if err != nil || seen[[2]int{pid, port}] {
				continue
			}
			seen[[2]int{pid, port}] = true
			res[pid] = append(res[pid], port)
		}
	}
	return res
}

// ParseLaunchctlList returns the PIDs of running jobs in `launchctl list`
// output ("PID\tStatus\tLabel", PID "-" when not running).
func ParseLaunchctlList(out []byte) map[int]bool {
	res := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
			res[pid] = true
		}
	}
	return res
}

// ParseProcArgs2 extracts allow-listed env vars from a KERN_PROCARGS2 buffer.
// Layout: int32 argc, exec path, NUL padding, argc argv strings, env strings.
func ParseProcArgs2(buf []byte) (map[string]string, error) {
	const argcSize = 4
	if len(buf) < argcSize {
		return nil, fmt.Errorf("procargs2 buffer too short: %d bytes", len(buf))
	}
	argc := int(binary.LittleEndian.Uint32(buf[:argcSize]))
	rest := buf[argcSize:]
	rest = skipString(rest) // exec path
	rest = bytes.TrimLeft(rest, "\x00")
	for i := 0; i < argc && len(rest) > 0; i++ {
		rest = skipString(rest)
	}
	env := map[string]string{}
	for len(rest) > 0 {
		end := bytes.IndexByte(rest, 0)
		if end == 0 {
			break
		}
		if end < 0 {
			end = len(rest)
		}
		kv := string(rest[:end])
		if k, v, ok := strings.Cut(kv, "="); ok && AttributionEnv[k] {
			env[k] = v
		}
		if end == len(rest) {
			break
		}
		rest = rest[end+1:]
	}
	return env, nil
}

func skipString(b []byte) []byte {
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return nil
	}
	return b[end+1:]
}
