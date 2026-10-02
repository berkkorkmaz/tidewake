package disk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"14.22GB": 14_220_000_000, "0B": 0, "512kB": 512_000, "3.5MB": 3_500_000}
	for in, want := range cases {
		if got, ok := ParseSize(in); !ok || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := ParseSize("lots"); ok {
		t.Error("expected failure")
	}
}

func TestParseDockerDF(t *testing.T) {
	out := []byte(`{"Active":"2","Reclaimable":"14.22GB (60%)","Size":"23.46GB","TotalCount":"20","Type":"Images"}
{"Active":"7","Reclaimable":"1.071GB (34%)","Size":"3.114GB","TotalCount":"32","Type":"Local Volumes"}
{"Active":"0","Reclaimable":"2.296GB","Size":"3.605GB","TotalCount":"60","Type":"Build Cache"}
not json
`)
	items := ParseDockerDF(out)
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Reclaimable != 14_220_000_000 || items[0].Suggest == "" {
		t.Errorf("images: %+v", items[0])
	}
	if items[1].Reclaimable != 0 || items[1].Suggest != "" {
		t.Errorf("volumes must never be suggested for removal: %+v", items[1])
	}
	if items[2].Suggest != "docker builder prune --max-used-space 10gb" {
		t.Errorf("build cache: %+v", items[2])
	}
}

func write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCodexPackagesKeepsCurrentAndRunning(t *testing.T) {
	home := t.TempDir()
	rel := filepath.Join(home, "packages", "standalone", "releases")
	write(t, filepath.Join(rel, "0.158.0", "codex"), 100)
	write(t, filepath.Join(rel, "0.159.0", "codex"), 200)
	write(t, filepath.Join(rel, "0.160.0", "codex"), 300)
	if err := os.Symlink(filepath.Join(rel, "0.160.0"), filepath.Join(home, "packages", "standalone", "current")); err != nil {
		t.Fatal(err)
	}
	running := []string{filepath.Join(rel, "0.159.0") + "/codex app-server"}
	items := CodexPackages(home, running)
	if len(items) != 1 || filepath.Base(items[0].Path) != "0.158.0" || items[0].Reclaimable != 100 {
		t.Fatalf("got %+v", items)
	}
}

func TestDirSizeSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a"), 10)
	outside := filepath.Join(t.TempDir(), "big")
	write(t, outside, 1000)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if got := DirSize(root); got != 10 {
		t.Fatalf("DirSize = %d, want 10", got)
	}
}
