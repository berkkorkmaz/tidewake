// Package worktree finds linked git worktrees and decides which are safe to remove.
package worktree

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/shell"
)

// Kind is the verdict for a worktree.
type Kind string

const (
	Removable Kind = "removable" // every safety check passed
	Dangling  Kind = "dangling"  // registered but its directory is gone
	Keep      Kind = "keep"      // at least one safety check failed
)

// DefaultIdle is how long a worktree must be untouched before removal is suggested.
const DefaultIdle = 48 * time.Hour

// regenerableDirs are rebuilt by a package manager or build, so deleting them loses no work.
var regenerableDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".venv": true, "venv": true, "target": true,
	"dist": true, "build": true, ".nuxt": true, ".next": true, ".output": true, ".turbo": true,
	"__pycache__": true, ".pytest_cache": true, ".gradle": true, "DerivedData": true,
	".ruff_cache": true, ".mypy_cache": true, ".tox": true, "htmlcov": true, ".parcel-cache": true,
	".svelte-kit": true,
}

// regenerableFiles are ignored files that hold no work.
var regenerableFiles = map[string]bool{".DS_Store": true, ".coverage": true, "coverage.xml": true}

// maxNamedIgnored caps how many ignored paths a reason names.
const maxNamedIgnored = 3

const evalWorkers = 8

// gitTimeout bounds one git call; a timed-out check counts as failed, which keeps the worktree.
const gitTimeout = 30 * time.Second

// Worktree is one linked worktree with its verdict.
type Worktree struct {
	Repo       string        `json:"repo"`
	Path       string        `json:"path"`
	Branch     string        `json:"branch,omitempty"`
	Owner      string        `json:"owner"`
	Kind       Kind          `json:"kind"`
	Locked     bool          `json:"locked"`
	Reasons    []string      `json:"reasons,omitempty"`
	SizeBytes  int64         `json:"sizeBytes"`
	RegenBytes int64         `json:"regenBytes"`
	Idle       time.Duration `json:"idleNs"`
	Suggest    string        `json:"suggest,omitempty"`
}

// Options configure a scan.
type Options struct {
	// Seeds are paths inside repos (session cwds, roots); each resolves to its repo.
	Seeds []string
	// InUse maps a directory to the processes whose cwd is inside it.
	InUse func(dir string) []string
	// OwnerPaths maps a harness name to path fragments that mark its worktrees.
	OwnerPaths map[string][]string
	Idle       time.Duration
	Now        time.Time
}

// Scan finds every linked worktree reachable from the seeds.
func Scan(ctx context.Context, opts Options) ([]Worktree, error) {
	repos := resolveRepos(ctx, opts.Seeds)
	var all []Worktree
	for _, repo := range repos {
		wts, err := listWorktrees(ctx, repo)
		if err != nil {
			continue // a repo we cannot read is skipped, not fatal
		}
		all = append(all, wts...)
	}
	evaluateAll(ctx, all, opts)
	sort.Slice(all, func(i, j int) bool {
		if all[i].Kind != all[j].Kind {
			return kindRank(all[i].Kind) < kindRank(all[j].Kind)
		}
		return all[i].SizeBytes > all[j].SizeBytes
	})
	return all, nil
}

func kindRank(k Kind) int {
	switch k {
	case Removable:
		return 0
	case Dangling:
		return 1
	}
	return 2
}

// git runs read-only: --no-optional-locks stops `git status` from rewriting
// the index, which would also reset the worktree's idle clock.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// resolveRepos maps seed paths to their main worktree, deduplicated.
func resolveRepos(ctx context.Context, seeds []string) []string {
	seen := map[string]bool{}
	var repos []string
	for _, s := range seeds {
		if s == "" {
			continue
		}
		common, err := git(ctx, s, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || seen[common] {
			continue
		}
		seen[common] = true
		main := common
		if filepath.Base(common) == ".git" {
			main = filepath.Dir(common)
		}
		repos = append(repos, main)
	}
	sort.Strings(repos)
	return repos
}

func listWorktrees(ctx context.Context, repo string) ([]Worktree, error) {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list in %s: %w", repo, err)
	}
	return ParsePorcelain(repo, out), nil
}

// ParsePorcelain parses `git worktree list --porcelain`, skipping the main worktree.
func ParsePorcelain(repo, out string) []Worktree {
	var res []Worktree
	for i, block := range strings.Split(out, "\n\n") {
		if i == 0 {
			continue // the main worktree is never a cleanup candidate
		}
		w := Worktree{Repo: repo}
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				w.Path = val
			case "branch":
				w.Branch = strings.TrimPrefix(val, "refs/heads/")
			case "locked":
				w.Locked = true
				w.Reasons = append(w.Reasons, strings.TrimSpace("locked "+val))
			}
		}
		if w.Path != "" {
			res = append(res, w)
		}
	}
	return res
}

func evaluateAll(ctx context.Context, wts []Worktree, opts Options) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < evalWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				evaluate(ctx, &wts[idx], opts)
			}
		}()
	}
	for i := range wts {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// slowWorktree is when TIDEWAKE_DEBUG reports a worktree's step timings.
const slowWorktree = 2 * time.Second

func evaluate(ctx context.Context, w *Worktree, opts Options) {
	steps := newStepTimer()
	defer steps.report(w.Path)
	w.Owner = owner(w.Path, opts.OwnerPaths)
	// git refuses to prune locked entries, so a locked one is kept even when its directory is gone.
	if _, err := os.Stat(w.Path); err != nil && w.Locked {
		w.Kind = Keep
		w.Reasons = append(w.Reasons, "directory no longer exists")
		return
	} else if err != nil {
		w.Kind = Dangling
		w.Reasons = []string{"directory no longer exists"}
		w.Suggest = "git -C " + shell.Quote(w.Repo) + " worktree prune"
		return
	}
	w.SizeBytes, w.RegenBytes = measure(w.Path)
	steps.mark("measure")
	w.Idle = idleFor(w.Path, opts.Now)

	resolved, err := filepath.EvalSymlinks(w.Path)
	if err != nil {
		resolved = w.Path
	}
	if users := opts.InUse(resolved); len(users) > 0 {
		w.Reasons = append(w.Reasons, "in use by "+strings.Join(users, ", "))
	}
	steps.mark("in-use")
	if status, err := git(ctx, w.Path, "status", "--porcelain", "--ignored", "--untracked-files=normal"); err != nil {
		w.Reasons = append(w.Reasons, "git status failed")
	} else {
		dirty, kept := ParseStatus(status)
		if dirty {
			w.Reasons = append(w.Reasons, "has uncommitted or untracked files")
		}
		// git worktree remove deletes ignored files too, so ignored work (.env, data) must block it.
		if len(kept) > 0 {
			w.Reasons = append(w.Reasons, "keeps ignored files that are not rebuildable: "+nameSome(kept))
		}
	}
	steps.mark("status")
	if pushed, err := onRemote(ctx, w.Path); err != nil {
		w.Reasons = append(w.Reasons, "could not check for unpushed commits")
	} else if !pushed {
		w.Reasons = append(w.Reasons, "has commits not on any remote")
	}
	steps.mark("on-remote")
	// Removing a worktree deletes its HEAD reflog, the only pointer to commits
	// made here and then left behind by a checkout.
	if reason := reflogOnlyCommits(ctx, w.Path); reason != "" {
		w.Reasons = append(w.Reasons, reason)
	}
	steps.mark("reflog")
	if w.Idle < opts.Idle {
		w.Reasons = append(w.Reasons, "touched "+humanDuration(w.Idle)+" ago")
	}

	if len(w.Reasons) == 0 {
		w.Kind = Removable
		w.Suggest = "git -C " + shell.Quote(w.Repo) + " worktree remove " + shell.Quote(w.Path)
		return
	}
	w.Kind = Keep
}

// ParseStatus reads `git status --porcelain --ignored` output. It returns
// whether there are uncommitted or untracked changes, and the ignored paths
// that are not rebuildable.
func ParseStatus(out string) (dirty bool, keptIgnored []string) {
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		code, path := line[:2], strings.TrimSuffix(line[3:], "/")
		if code != "!!" {
			dirty = true
			continue
		}
		if !rebuildable(path) {
			keptIgnored = append(keptIgnored, path)
		}
	}
	return dirty, keptIgnored
}

func rebuildable(path string) bool {
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		if regenerableDirs[part] {
			return true
		}
	}
	last := parts[len(parts)-1]
	return regenerableDirs[last] || regenerableFiles[last] || strings.HasSuffix(last, ".pyc")
}

func nameSome(paths []string) string {
	if len(paths) <= maxNamedIgnored {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:maxNamedIgnored], ", ") + fmt.Sprintf(" and %d more", len(paths)-maxNamedIgnored)
}

// maxReflogChecks caps the commits checked per worktree; more than this keeps it.
const maxReflogChecks = 25

// commitActions are reflog subjects that record a newly created commit.
var commitActions = []string{"commit", "merge", "cherry-pick", "rebase"}

// reflogOnlyCommits returns a reason when commits created in this worktree
// are reachable from no branch, tag or remote.
func reflogOnlyCommits(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "reflog", "--format=%H %gs", "HEAD")
	if err != nil {
		return "" // a worktree with no reflog has no commits to lose
	}
	seen := map[string]bool{}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		sha, subject, ok := strings.Cut(line, " ")
		if !ok || seen[sha] || !isCommitAction(subject) {
			continue
		}
		seen[sha] = true
		shas = append(shas, sha)
	}
	if len(shas) > maxReflogChecks {
		return fmt.Sprintf("%d commits made here; check `git reflog` before removing", len(shas))
	}
	lost := 0
	for _, sha := range shas {
		ref, err := git(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", sha,
			"refs/heads", "refs/remotes", "refs/tags")
		if err != nil || ref == "" {
			lost++
		}
	}
	if lost == 0 {
		return ""
	}
	return fmt.Sprintf("%d commit(s) made here are on no branch, tag or remote", lost)
}

func isCommitAction(subject string) bool {
	for _, a := range commitActions {
		if strings.HasPrefix(subject, a) {
			return true
		}
	}
	return false
}

// onRemote reports whether HEAD is contained in some remote-tracking branch,
// i.e. every commit is pushed. `for-each-ref --contains` uses the commit graph
// and stays fast; `rev-list HEAD --not --remotes` took minutes on large repos.
func onRemote(ctx context.Context, dir string) (bool, error) {
	out, err := git(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", "HEAD", "refs/remotes")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func owner(path string, ownerPaths map[string][]string) string {
	for name, frags := range ownerPaths {
		for _, f := range frags {
			if strings.Contains(path, f) {
				return name
			}
		}
	}
	return "git"
}

// measure returns total bytes and bytes inside regenerable dirs, without following symlinks.
func measure(root string) (total, regen int64) {
	var walk func(dir string, inRegen bool)
	walk = func(dir string, inRegen bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(p, inRegen || regenerableDirs[e.Name()])
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			total += info.Size()
			if inRegen {
				regen += info.Size()
			}
		}
	}
	walk(root, false)
	return total, regen
}

// idleFor measures time since git last touched the worktree's index or HEAD.
func idleFor(path string, now time.Time) time.Duration {
	gitdir := adminDir(path)
	var newest time.Time
	for _, name := range []string{"index", "HEAD", "logs/HEAD"} {
		if info, err := os.Stat(filepath.Join(gitdir, name)); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return 0 // unknown counts as just touched, which blocks removal
	}
	return now.Sub(newest)
}

func adminDir(path string) string {
	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return filepath.Join(path, ".git")
	}
	dir := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return dir
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

type stepTimer struct {
	on    bool
	start time.Time
	last  time.Time
	parts []string
}

func newStepTimer() *stepTimer {
	now := time.Now()
	return &stepTimer{on: os.Getenv("TIDEWAKE_DEBUG") != "", start: now, last: now}
}

func (s *stepTimer) mark(name string) {
	if !s.on {
		return
	}
	now := time.Now()
	s.parts = append(s.parts, name+"="+now.Sub(s.last).Round(time.Millisecond).String())
	s.last = now
}

func (s *stepTimer) report(path string) {
	if s.on && time.Since(s.start) > slowWorktree {
		fmt.Fprintf(os.Stderr, "tidewake:   slow worktree %s: %s\n", path, strings.Join(s.parts, " "))
	}
}
