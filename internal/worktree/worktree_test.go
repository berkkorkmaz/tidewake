package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ageAdmin makes git's view of a worktree old, as if untouched for d.
func ageAdmin(t *testing.T, wt string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	admin := adminDir(wt)
	for _, name := range []string{"index", "HEAD", "logs/HEAD"} {
		_ = os.Chtimes(filepath.Join(admin, name), old, old)
	}
}

type repoFixture struct {
	main string
	root string
}

func newRepo(t *testing.T) *repoFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	main := filepath.Join(root, "main")
	run(t, root, "init", "-q", "--bare", remote)
	run(t, root, "init", "-q", "-b", "main", main)
	for name, body := range map[string]string{"a.txt": "a", ".gitignore": "node_modules\n"} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, main, "add", "a.txt", ".gitignore")
	run(t, main, "commit", "-q", "-m", "init")
	run(t, main, "remote", "add", "origin", remote)
	run(t, main, "push", "-q", "origin", "main")
	return &repoFixture{main: main, root: root}
}

func (r *repoFixture) worktree(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(r.root, "wt", name)
	run(t, r.main, "worktree", "add", "-q", "--detach", path, "main")
	return path
}

func scan(t *testing.T, r *repoFixture, inUse map[string]bool) map[string]Worktree {
	t.Helper()
	wts, err := Scan(context.Background(), Options{
		Seeds: []string{r.main},
		InUse: func(dir string) []string {
			if inUse[dir] {
				return []string{"node (pid 42)"}
			}
			return nil
		},
		OwnerPaths: map[string][]string{"claude": {"/.claude/worktrees/"}},
		Idle:       DefaultIdle,
		Now:        time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Worktree{}
	for _, w := range wts {
		out[filepath.Base(w.Path)] = w
	}
	return out
}

func TestScanVerdicts(t *testing.T) {
	r := newRepo(t)
	week := 7 * 24 * time.Hour

	clean := r.worktree(t, "clean")
	if err := os.MkdirAll(filepath.Join(clean, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clean, "node_modules", "x", "big.js"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	dirty := r.worktree(t, "dirty")
	if err := os.WriteFile(filepath.Join(dirty, "new.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}

	unpushed := r.worktree(t, "unpushed")
	if err := os.WriteFile(filepath.Join(unpushed, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, unpushed, "commit", "-q", "-am", "local only")

	locked := r.worktree(t, "locked")
	run(t, r.main, "worktree", "lock", "--reason", "agent running", locked)

	r.worktree(t, "fresh")
	busy := r.worktree(t, "busy")
	gone := r.worktree(t, "gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	for _, wt := range []string{clean, dirty, unpushed, locked, busy} {
		ageAdmin(t, wt, week)
	}

	busyResolved, err := filepath.EvalSymlinks(busy) // cwds from lsof are resolved paths
	if err != nil {
		t.Fatal(err)
	}
	got := scan(t, r, map[string]bool{busyResolved: true})
	want := map[string]struct {
		kind   Kind
		reason string
	}{
		"clean":    {Removable, ""},
		"dirty":    {Keep, "uncommitted or untracked"},
		"unpushed": {Keep, "not on any remote"},
		"locked":   {Keep, "locked agent running"},
		"fresh":    {Keep, "touched"},
		"busy":     {Keep, "in use by node (pid 42)"},
		"gone":     {Dangling, "no longer exists"},
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: missing from scan", name)
			continue
		}
		if g.Kind != w.kind || !strings.Contains(strings.Join(g.Reasons, "; "), w.reason) {
			t.Errorf("%s: kind=%s reasons=%v, want %s containing %q", name, g.Kind, g.Reasons, w.kind, w.reason)
		}
	}
	if len(got) != len(want) {
		t.Errorf("scan returned %d worktrees, want %d (main must be excluded)", len(got), len(want))
	}
	c := got["clean"]
	if c.RegenBytes != 4096 || c.SizeBytes < c.RegenBytes {
		t.Errorf("clean sizes: total=%d regen=%d", c.SizeBytes, c.RegenBytes)
	}
	if !strings.HasPrefix(c.Suggest, "git -C ") || !strings.Contains(c.Suggest, "worktree remove") {
		t.Errorf("clean suggest = %q", c.Suggest)
	}
	if got["gone"].Suggest == "" || !strings.HasSuffix(got["gone"].Suggest, "worktree prune") {
		t.Errorf("gone suggest = %q", got["gone"].Suggest)
	}
}

func TestScanDoesNotTouchIdleClock(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "w")
	ageAdmin(t, wt, 7*24*time.Hour)
	first := scan(t, r, nil)["w"].Idle
	second := scan(t, r, nil)["w"].Idle
	if second < first {
		t.Fatalf("scanning reset the idle clock: %v then %v", first, second)
	}
}

func TestLockedAndMissingIsKeptNotPruned(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "lg")
	run(t, r.main, "worktree", "lock", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	g := scan(t, r, nil)["lg"]
	if g.Kind != Keep || g.Suggest != "" {
		t.Fatalf("got %+v", g)
	}
}

func TestParsePorcelainSkipsMain(t *testing.T) {
	out := "worktree /r\nHEAD abc\nbranch refs/heads/main\n\nworktree /r/wt/a\nHEAD def\nbranch refs/heads/feat\nlocked\n\nworktree /r/wt/b\nHEAD 123\ndetached\nprunable gitdir file points to non-existent location\n"
	wts := ParsePorcelain("/r", out)
	if len(wts) != 2 || wts[0].Branch != "feat" || !wts[0].Locked || wts[1].Locked {
		t.Fatalf("got %+v", wts)
	}
}

func TestParseStatus(t *testing.T) {
	out := " M a.txt\n!! node_modules/\n!! infra/prod/.terraform/\n!! dbt_packages/\n!! .env\n!! flows/.pytest_cache/\n!! data/dump.parquet\n!! pkg/__pycache__/x.pyc\n!! .DS_Store\n"
	dirty, kept := ParseStatus(out)
	if !dirty {
		t.Error("modified file not seen")
	}
	want := []string{".env", "data/dump.parquet"}
	if strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Errorf("kept = %v, want %v", kept, want)
	}
	if d, k := ParseStatus("!! node_modules/\n"); d || len(k) != 0 {
		t.Errorf("only rebuildable ignored files: dirty=%v kept=%v", d, k)
	}
}

func TestIgnoredEnvBlocksRemoval(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "withenv")
	if err := os.WriteFile(filepath.Join(r.main, ".gitignore"), []byte("node_modules\n.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("node_modules\n.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "commit", "-q", "-am", "ignore env")
	run(t, wt, "push", "-q", "origin", "HEAD:refs/heads/withenv")
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("SECRET=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ageAdmin(t, wt, 7*24*time.Hour)
	g := scan(t, r, nil)["withenv"]
	if g.Kind != Keep || !strings.Contains(strings.Join(g.Reasons, ";"), ".env") {
		t.Fatalf("got kind=%s reasons=%v", g.Kind, g.Reasons)
	}
}

func TestCommitsLeftOnlyInReflogBlockRemoval(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "detached")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("agent work"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "commit", "-q", "-am", "agent commit")
	run(t, wt, "checkout", "-q", "--detach", "origin/main") // HEAD is pushed again, the commit is orphaned
	ageAdmin(t, wt, 7*24*time.Hour)
	g := scan(t, r, nil)["detached"]
	if g.Kind != Keep || !strings.Contains(strings.Join(g.Reasons, ";"), "on no branch, tag or remote") {
		t.Fatalf("got kind=%s reasons=%v", g.Kind, g.Reasons)
	}
}

func TestPushedCommitInReflogIsFine(t *testing.T) {
	r := newRepo(t)
	wt := r.worktree(t, "pushed")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("shared work"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, wt, "commit", "-q", "-am", "pushed commit")
	run(t, wt, "push", "-q", "origin", "HEAD:refs/heads/feature")
	ageAdmin(t, wt, 7*24*time.Hour)
	if g := scan(t, r, nil)["pushed"]; g.Kind != Removable {
		t.Fatalf("got kind=%s reasons=%v", g.Kind, g.Reasons)
	}
}
