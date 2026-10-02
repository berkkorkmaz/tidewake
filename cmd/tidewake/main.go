// Command tidewake finds what Claude Code and Codex sessions left behind.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/disk"
	"github.com/berkkorkmaz/tidewake/internal/doctor"
	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/health"
	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/report"
	"github.com/berkkorkmaz/tidewake/internal/rules"
	"github.com/berkkorkmaz/tidewake/internal/scan"
	"github.com/berkkorkmaz/tidewake/internal/worktree"
)

// version is set at release time with -ldflags.
var version = "dev"

// commandTimeout bounds each external command (claude, docker, sqlite3).
const commandTimeout = 20 * time.Second

const usage = `tidewake finds what your Claude Code and Codex sessions left behind.

Usage:
  tidewake scan    [--json] [--all] [--root DIR]... [--idle 48h] [--codex-idle 24h]
  tidewake doctor  [--json]
  tidewake sessions [--json] [--stuck 2h] [--idle 24h] [--ram 4GB] [--sample 3s]
  tidewake version

Every command is read-only: none of them stops a process or deletes a file.
`

type rootList []string

func (r *rootList) String() string     { return fmt.Sprint(*r) }
func (r *rootList) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "scan":
		return cmdScan(ctx, args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(ctx, args[1:], stdout, stderr)
	case "sessions":
		return cmdSessions(ctx, args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "tidewake", version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func scanOptions(roots []string, idle time.Duration) (scan.Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return scan.Options{}, err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	return scan.Options{
		ClaudeHome: claudeHome, CodexHome: codexHome, Roots: roots, Idle: idle,
		Collector: proc.System{}, Run: harness.ExecRunner(commandTimeout),
	}, nil
}

func cmdScan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	all := fs.Bool("all", false, "also list kept worktrees and detached processes")
	idle := fs.Duration("idle", worktree.DefaultIdle, "minimum idle time before a worktree is suggested for removal")
	codexIdle := fs.Duration("codex-idle", harness.DefaultCodexIdle,
		"how long a Codex thread may sit idle and still count as running (raise it for week-long runs)")
	var roots rootList
	fs.Var(&roots, "root", "extra directory inside a repo to check for worktrees (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *codexIdle <= 0 {
		fmt.Fprintln(stderr, "tidewake: --codex-idle must be positive, e.g. 24h or 168h")
		return 2
	}
	opts, err := scanOptions(roots, *idle)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	opts.CodexIdle = *codexIdle
	res, err := scan.Run(ctx, opts)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	if *asJSON {
		return writeJSON(stdout, stderr, res)
	}
	home, _ := os.UserHomeDir()
	report.Scan(stdout, res, report.Options{All: *all, Home: home})
	return 0
}

func cmdDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts, err := scanOptions(nil, worktree.DefaultIdle)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	pack, err := rules.Load()
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	snap, err := opts.Collector.Collect(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	st := harness.Load(ctx, harness.Options{
		ClaudeHome: opts.ClaudeHome, CodexHome: opts.CodexHome, Run: opts.Run, Now: snap.Taken,
	}, snap, pack)
	rep := doctor.Run(ctx, pack, opts.Run, snap, st)
	if *asJSON {
		return writeJSON(stdout, stderr, rep)
	}
	report.Doctor(stdout, rep)
	return 0
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	return 0
}

// defaultSample is how long CPU use is measured; long enough to tell a
// working session from a silent one.
const defaultSample = 3 * time.Second

func cmdSessions(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	limits := health.DefaultThresholds
	fs.DurationVar(&limits.Stuck, "stuck", limits.Stuck, "busy this long with no activity counts as stuck")
	fs.DurationVar(&limits.Idle, "idle", limits.Idle, "idle this long while holding 1 GB+ gets a flag")
	ram := fs.String("ram", "4GB", "memory per session worth flagging, e.g. 4GB or 800MB")
	sample := fs.Duration("sample", defaultSample, "how long to measure CPU use")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ramBytes, ok := disk.ParseSize(*ram)
	if !ok || ramBytes <= 0 {
		fmt.Fprintf(stderr, "tidewake: --ram %q: use a size like 4GB or 800MB\n", *ram)
		return 2
	}
	limits.RAMKB = health.KiBFromBytes(ramBytes)

	opts, err := scanOptions(nil, worktree.DefaultIdle)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	pack, err := rules.Load()
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	sys := proc.System{}
	snap, err := sys.Collect(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	select {
	case <-time.After(*sample):
	case <-ctx.Done():
		return 1
	}
	after, err := sys.SampleCPU(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "tidewake:", err)
		return 1
	}
	sessions, _ := health.ReadClaudeSessions(filepath.Join(opts.ClaudeHome, "sessions"))
	now := time.Now()
	rows := health.Report(health.Input{
		Snap: snap, CPUAfter: after, Interval: *sample, Sessions: sessions,
		Activity: health.TranscriptActivity(filepath.Join(opts.ClaudeHome, "projects")),
		Pack:     pack, Limits: limits, Now: now,
	})
	if *asJSON {
		if rows == nil {
			rows = []health.Row{}
		}
		return writeJSON(stdout, stderr, rows)
	}
	home, _ := os.UserHomeDir()
	report.Sessions(stdout, rows, report.SessionsOptions{Home: home, Now: now, Limits: limits, Sample: *sample})
	return 0
}
