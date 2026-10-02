// Package watch finds new harness release notes that matter to tidewake:
// leaks, orphaned processes, worktrees and cleanup. A person reviews the
// candidates before anything enters the rules pack.
package watch

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// relevant matches release-note lines worth a human look.
var relevant = regexp.MustCompile(`(?i)orphan|leak|left running|zombie|worktree|process tree|reap|clean ?up|memory|daemon|mcp server|child process|session ?end|retention`)

var changelogHeading = regexp.MustCompile(`^## +v?(\d+\.\d+\.\d+)`)

// Release is one version's notes.
type Release struct {
	Version string
	Lines   []string
}

// Candidate is a relevant line from a release newer than the last review.
type Candidate struct {
	Harness string
	Version string
	Line    string
}

// ParseChangelog splits a Keep-a-Changelog style file into releases.
func ParseChangelog(text string) []Release {
	var out []Release
	var cur *Release
	for _, line := range strings.Split(text, "\n") {
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			out = append(out, Release{Version: m[1]})
			cur = &out[len(out)-1]
			continue
		}
		if cur != nil && strings.HasPrefix(strings.TrimSpace(line), "- ") {
			cur.Lines = append(cur.Lines, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")))
		}
	}
	return out
}

// GitHubRelease is the subset of the GitHub releases API tidewake reads.
type GitHubRelease struct {
	TagName    string `json:"tag_name"`
	Body       string `json:"body"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// FromGitHubReleases converts stable GitHub releases into releases.
func FromGitHubReleases(rs []GitHubRelease) []Release {
	var out []Release
	for _, r := range rs {
		if r.Prerelease || r.Draft {
			continue
		}
		v, err := rules.ParseVersion(r.TagName)
		if err != nil {
			continue
		}
		rel := Release{Version: v.String()}
		for _, line := range strings.Split(r.Body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				rel.Lines = append(rel.Lines, strings.TrimSpace(line[2:]))
			}
		}
		out = append(out, rel)
	}
	return out
}

// Newer returns relevant lines from releases newer than since, and the newest
// version seen (since itself when nothing is newer).
func Newer(harness string, releases []Release, since string) ([]Candidate, string, error) {
	last, err := rules.ParseVersion(since)
	if err != nil {
		return nil, "", fmt.Errorf("watch state for %s: %w", harness, err)
	}
	newest := last
	var out []Candidate
	for _, r := range releases {
		v, err := rules.ParseVersion(r.Version)
		if err != nil || !last.Less(v) {
			continue
		}
		if newest.Less(v) {
			newest = v
		}
		for _, line := range r.Lines {
			if relevant.MatchString(line) {
				out = append(out, Candidate{Harness: harness, Version: v.String(), Line: line})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := rules.ParseVersion(out[i].Version)
		b, _ := rules.ParseVersion(out[j].Version)
		return a.Less(b)
	})
	return out, newest.String(), nil
}

// Markdown renders candidates for a pull request body.
func Markdown(cands []Candidate, labels map[string]string) string {
	if len(cands) == 0 {
		return "No new release notes about leaks, orphans, worktrees or cleanup.\n"
	}
	var b strings.Builder
	b.WriteString("New release notes that may change tidewake's rules pack. Review each line:\n")
	b.WriteString("add fixed issues with `fixedIn`, mark open issues fixed, or update signatures.\n")
	group := ""
	for _, c := range cands {
		head := labels[c.Harness] + " " + c.Version
		if head != group {
			b.WriteString("\n### " + head + "\n\n")
			group = head
		}
		b.WriteString("- [ ] " + c.Line + "\n")
	}
	return b.String()
}
