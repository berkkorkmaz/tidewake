package watch

import (
	"strings"
	"testing"
)

const changelog = `# Changelog

## 2.1.288

- Fixed stdio MCP servers being left running after /exit
- Improved syntax highlighting

## 2.1.287

- Fixed orphaned worktrees after a crash
- New theme

## 2.1.200

- Fixed memory leak in long sessions
`

func TestNewerFromChangelog(t *testing.T) {
	cands, newest, err := Newer("claude", ParseChangelog(changelog), "2.1.286")
	if err != nil {
		t.Fatal(err)
	}
	if newest != "2.1.288" {
		t.Fatalf("newest = %s", newest)
	}
	if len(cands) != 2 || cands[0].Version != "2.1.287" || !strings.Contains(cands[1].Line, "left running") {
		t.Fatalf("cands = %+v", cands)
	}
}

func TestNothingNewKeepsVersion(t *testing.T) {
	cands, newest, err := Newer("claude", ParseChangelog(changelog), "2.1.288")
	if err != nil || len(cands) != 0 || newest != "2.1.288" {
		t.Fatalf("cands=%v newest=%s err=%v", cands, newest, err)
	}
	if !strings.Contains(Markdown(nil, nil), "No new release notes") {
		t.Fatal("empty markdown")
	}
}

func TestGitHubReleasesSkipPrereleases(t *testing.T) {
	rs := []GitHubRelease{
		{TagName: "rust-v0.161.0", Body: "## Changes\n- #1 Reap orphaned MCP children\n- #2 New model picker"},
		{TagName: "rust-v0.162.0-alpha.1", Body: "- #3 zombie fix", Prerelease: true},
		{TagName: "rust-v0.160.0", Body: "- #4 worktree cleanup"},
	}
	cands, newest, err := Newer("codex", FromGitHubReleases(rs), "0.160.0")
	if err != nil {
		t.Fatal(err)
	}
	if newest != "0.161.0" || len(cands) != 1 || !strings.Contains(cands[0].Line, "Reap") {
		t.Fatalf("newest=%s cands=%+v", newest, cands)
	}
	md := Markdown(cands, map[string]string{"codex": "Codex"})
	if !strings.Contains(md, "### Codex 0.161.0") || !strings.Contains(md, "- [ ] #1 Reap") {
		t.Fatalf("markdown:\n%s", md)
	}
}

func TestBadStateVersion(t *testing.T) {
	if _, _, err := Newer("claude", nil, "latest"); err == nil {
		t.Fatal("expected error")
	}
}
