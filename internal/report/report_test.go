package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/attrib"
	"github.com/berkkorkmaz/tidewake/internal/disk"
	"github.com/berkkorkmaz/tidewake/internal/doctor"
	"github.com/berkkorkmaz/tidewake/internal/rules"
	"github.com/berkkorkmaz/tidewake/internal/scan"
	"github.com/berkkorkmaz/tidewake/internal/worktree"
)

func TestScanReportTotalsAndHiding(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := &scan.Result{
		Taken: now, RulesVersion: "t",
		Processes: []attrib.Finding{
			{Kind: attrib.Leftover, Harness: "claude", SessionID: "abcdef123456", PID: 10, Command: "node mcp.js",
				Started: now.Add(-72 * time.Hour), TreePIDs: []int{10, 11}, TreeRSSKB: 2000, Proof: []string{"p1", "p2"}, Suggest: "kill -TERM 10 11"},
			{Kind: attrib.Detached, Harness: "claude", PID: 20, Command: "vite", TreeRSSKB: 999999},
		},
		Worktrees: []worktree.Worktree{
			{Kind: worktree.Removable, Owner: "claude", Path: "/home/me/r/wt", SizeBytes: 3_000_000_000, Suggest: "git -C /home/me/r worktree remove /home/me/r/wt"},
			{Kind: worktree.Dangling, Repo: "/home/me/r", Suggest: "git -C /home/me/r worktree prune"},
			{Kind: worktree.Dangling, Repo: "/home/me/r", Suggest: "git -C /home/me/r worktree prune"},
			{Kind: worktree.Keep, Reasons: []string{"touched 3h ago"}},
		},
		Disk:    []disk.Item{{Category: "docker images", Bytes: 5_000_000_000, Reclaimable: 1_000_000_000, Suggest: "docker image prune"}},
		Sources: map[string]string{"codex threads db": "no db", "claude agents --json": "ok"},
	}
	var buf bytes.Buffer
	Scan(&buf, r, Options{Home: "/home/me"})
	out := buf.String()
	for _, want := range []string{
		"leftover", "kill -TERM 10 11", "tree: 2 processes", "why:  p1; p2",
		"+ 1 detached", "~/r/wt", "2 registration(s) in ~/r", "kept: touched recently 1",
		"TOTAL  4.0 GB disk and 2.0 MB RAM", "Not read:", "codex threads db", "no db",
		"Nothing was changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "vite") {
		t.Error("detached process listed without --all")
	}
	buf.Reset()
	Scan(&buf, r, Options{All: true})
	if !strings.Contains(buf.String(), "vite") || !strings.Contains(buf.String(), "kept      ") {
		t.Error("--all must list detached processes and kept worktrees")
	}
}

func TestDoctorReport(t *testing.T) {
	rep := doctor.Report{
		RulesVersion: "t",
		Harnesses: []doctor.HarnessReport{
			{Label: "Claude Code", Installed: true, Version: "2.1.250", Fixed: 3,
				Affected: []rules.Issue{{Title: "leak A", FixedIn: "2.1.283"}},
				Open:     []rules.Issue{{Title: "leak B", URL: "https://x", Covers: "scan lists it"}}},
			{Label: "Codex"},
		},
		MCPDuplicates: []doctor.MCPGroup{{Command: "node mcp", Copies: 3, Sessions: 3, RSSKB: 1000}},
		Sources:       map[string]string{"a": "ok"},
	}
	var buf bytes.Buffer
	Doctor(&buf, rep)
	out := buf.String()
	for _, want := range []string{"Claude Code 2.1.250", "UPGRADE  leak A (fixed in 2.1.283)", "OPEN     leak B · tidewake: scan lists it",
		"Codex: not found", "3 copies in 3 session(s)", "MCP gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor missing %q\n%s", want, out)
		}
	}
}

func TestSize(t *testing.T) {
	cases := map[int64]string{999: "999 B", 1000: "1.0 kB", 1_500_000: "1.5 MB", 27_600_000_000: "27.6 GB"}
	for in, want := range cases {
		if got := size(in); got != want {
			t.Errorf("size(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayCommand(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/Cellar/python@3.14/Frameworks/Python.framework/Versions/3.14/Resources/Python.app/Contents/MacOS/Python -m http.server 8765": "Python -m http.server 8765",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser --headless=new":                                                               "Brave Browser --headless=new",
		"node scripts/start.mjs": "node scripts/start.mjs",
		"/usr/bin/tail":          "tail",
	}
	for in, want := range cases {
		if got := displayCommand(in); got != want {
			t.Errorf("displayCommand(%q) = %q, want %q", in, got, want)
		}
	}
}
