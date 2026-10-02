//go:build darwin

package proc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// System collects a snapshot from the live macOS process table.
type System struct{}

// Collect runs ps and lsof, then reads attribution env vars per process.
func (System) Collect(ctx context.Context) (*Snapshot, error) {
	uid := strconv.Itoa(os.Getuid())
	type result struct {
		out []byte
		err error
	}
	run := func(name string, args ...string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			cmd := exec.CommandContext(ctx, name, args...)
			// ps prints localized month names otherwise (e.g. "Eki" under tr_TR).
			cmd.Env = append(os.Environ(), "LC_ALL=C")
			out, err := cmd.Output()
			ch <- result{out, err}
		}()
		return ch
	}
	psCh := run("ps", PSArgs...)
	cwdCh := run("lsof", "-nP", "-w", "-a", "-u", uid, "-d", "cwd", "-F", "pn")
	portCh := run("lsof", "-nP", "-w", "-a", "-u", uid, "-iTCP", "-sTCP:LISTEN", "-F", "pn")
	jobsCh := run("launchctl", "list")

	start := time.Now()
	ps := <-psCh
	if ps.err != nil {
		return nil, fmt.Errorf("ps: %w", ps.err)
	}
	procs, skipped, err := ParsePS(ps.out, time.Local)
	if err != nil {
		return nil, err
	}
	if skipped > 0 && os.Getenv("TIDEWAKE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "tidewake:   skipped %d unparsable ps lines\n", skipped)
	}
	// lsof exits 1 when some processes are unreadable; partial output is still valid.
	cwds := ParseLsofPathByPID((<-cwdCh).out)
	portOut := (<-portCh).out
	ports := ParseLsofPorts(portOut)
	exposed := ParseLsofExposed(portOut)
	jobs := ParseLaunchctlList((<-jobsCh).out)

	if os.Getenv("TIDEWAKE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "tidewake:   ps+lsof     %s\n", time.Since(start).Round(time.Millisecond))
	}
	me := os.Getuid()
	for pid, p := range procs {
		p.Cwd = cwds[pid]
		p.Ports = ports[pid]
		p.Exposed = exposed[pid]
		p.LaunchdJob = jobs[pid]
		if p.UID == me {
			p.Env, p.Exe = readArgs(pid)
		}
	}
	return &Snapshot{Taken: time.Now(), Procs: procs}, nil
}

func readArgs(pid int) (map[string]string, string) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, ""
	}
	env, err := ParseProcArgs2(buf)
	if err != nil {
		return nil, ""
	}
	return env, ProcArgs2Exe(buf)
}
