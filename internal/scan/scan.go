// Package scan runs every read-only check and assembles one result.
package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/attrib"
	"github.com/berkkorkmaz/tidewake/internal/disk"
	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
	"github.com/berkkorkmaz/tidewake/internal/worktree"
)

// Options configure a scan.
type Options struct {
	ClaudeHome string
	CodexHome  string
	Roots      []string // extra paths whose repos are scanned for worktrees
	Idle       time.Duration
	Collector  proc.Collector
	Run        harness.Runner
}

// Result is everything a scan found.
type Result struct {
	Taken        time.Time           `json:"taken"`
	RulesVersion string              `json:"rulesVersion"`
	Processes    []attrib.Finding    `json:"processes"`
	Worktrees    []worktree.Worktree `json:"worktrees"`
	Disk         []disk.Item         `json:"disk"`
	Sources      map[string]string   `json:"sources"`
	// Shared with doctor so it does not take a second snapshot.
	Snapshot *proc.Snapshot `json:"-"`
	State    *harness.State `json:"-"`
	Pack     *rules.Pack    `json:"-"`
}

// stageTimer prints how long each stage took when TIDEWAKE_DEBUG is set.
func stageTimer() func(name string) {
	if os.Getenv("TIDEWAKE_DEBUG") == "" {
		return func(string) {}
	}
	last := time.Now()
	return func(name string) {
		fmt.Fprintf(os.Stderr, "tidewake: %-12s %s\n", name, time.Since(last).Round(time.Millisecond))
		last = time.Now()
	}
}

// maxInUseNames caps how many processes are named per in-use worktree.
const maxInUseNames = 3

// Run performs the scan. It never modifies anything.
func Run(ctx context.Context, opts Options) (*Result, error) {
	pack, err := rules.Load()
	if err != nil {
		return nil, err
	}
	stage := stageTimer()
	snap, err := opts.Collector.Collect(ctx)
	if err != nil {
		return nil, err
	}
	stage("processes")
	st := harness.Load(ctx, harness.Options{
		ClaudeHome: opts.ClaudeHome, CodexHome: opts.CodexHome, Run: opts.Run, Now: snap.Taken,
	}, snap, pack)

	res := &Result{Taken: snap.Taken, RulesVersion: pack.Version, Sources: map[string]string{},
		Snapshot: snap, State: st, Pack: pack}
	stage("sessions")
	res.Processes = attrib.Classify(attrib.Input{Snap: snap, State: st, Pack: pack, UID: os.Getuid(), Self: os.Getpid()})
	stage("classify")

	res.Worktrees, err = worktree.Scan(ctx, worktree.Options{
		Seeds:      worktreeSeeds(snap, st, pack, opts.Roots),
		InUse:      inUseIndex(snap),
		OwnerPaths: ownerPaths(pack),
		Idle:       opts.Idle,
		Now:        snap.Taken,
	})
	if err != nil {
		return nil, err
	}
	stage("worktrees")

	res.Disk = append(res.Disk, disk.CodexPackages(opts.CodexHome, runningPaths(ctx, snap, opts))...)
	stage("codex pkgs")
	if items, err := disk.Docker(ctx, opts.Run); err == nil {
		res.Disk = append(res.Disk, items...)
	} else {
		st.Sources["docker system df"] = err
	}
	stage("docker")
	res.Disk = append(res.Disk, disk.HarnessStores(opts.ClaudeHome, opts.CodexHome)...)
	stage("stores")

	for src, e := range st.Sources {
		res.Sources[src] = "ok"
		if e != nil {
			res.Sources[src] = e.Error()
		}
	}
	return res, nil
}

// worktreeSeeds collects every directory that may sit inside a repo with agent worktrees.
func worktreeSeeds(snap *proc.Snapshot, st *harness.State, pack *rules.Pack, roots []string) []string {
	seen := map[string]bool{}
	var seeds []string
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		if _, err := os.Stat(dir); err != nil {
			return
		}
		seen[dir] = true
		seeds = append(seeds, dir)
	}
	for _, r := range roots {
		add(r)
	}
	for _, s := range st.Sessions {
		add(s.Cwd)
	}
	for _, p := range snap.Procs {
		for _, h := range pack.Harnesses {
			if h.IsHarnessProcess(p.Name(), p.Command) {
				add(p.Cwd)
			}
		}
	}
	sort.Strings(seeds)
	return seeds
}

// inUseIndex answers which processes have their cwd inside a directory.
func inUseIndex(snap *proc.Snapshot) func(string) []string {
	return func(dir string) []string {
		var users []string
		for _, p := range snap.Procs {
			if p.Cwd == dir || strings.HasPrefix(p.Cwd, dir+string(filepath.Separator)) {
				users = append(users, fmt.Sprintf("%s (pid %d)", p.Name(), p.PID))
			}
		}
		sort.Strings(users)
		if len(users) > maxInUseNames {
			users = append(users[:maxInUseNames], fmt.Sprintf("%d more", len(users)-maxInUseNames))
		}
		return users
	}
}

func ownerPaths(pack *rules.Pack) map[string][]string {
	out := map[string][]string{}
	for name, h := range pack.Harnesses {
		out[name] = h.WorktreePaths
	}
	return out
}

// runningPaths returns every command line plus the real executable of any
// process launched through a `current` symlink, which hides its release.
func runningPaths(ctx context.Context, snap *proc.Snapshot, opts Options) []string {
	out := make([]string, 0, len(snap.Procs))
	var viaCurrent []string
	for _, p := range snap.Procs {
		out = append(out, p.Command)
		if strings.Contains(p.Command, filepath.Join(opts.CodexHome, "packages")) && strings.Contains(p.Command, "/current/") {
			viaCurrent = append(viaCurrent, strconv.Itoa(p.PID))
		}
	}
	if len(viaCurrent) == 0 {
		return out
	}
	txt, err := opts.Run(ctx, "lsof", "-nP", "-w", "-a", "-d", "txt", "-p", strings.Join(viaCurrent, ","), "-F", "pn")
	if err != nil && len(txt) == 0 {
		// Cannot tell which release they run: report every release as in use.
		return append(out, filepath.Join(opts.CodexHome, "packages")+"/")
	}
	return append(out, proc.ParseLsofPaths(txt)...)
}
