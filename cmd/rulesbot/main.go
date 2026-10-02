// Command rulesbot checks Claude Code and Codex release notes for changes
// that affect tidewake's rules pack. CI runs it daily and opens a pull request
// for a person to review; it never edits the rules pack itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/watch"
)

const (
	claudeChangelogURL = "https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md"
	codexReleasesURL   = "https://api.github.com/repos/openai/codex/releases?per_page=50"
	fetchTimeout       = 30 * time.Second
	maxBodyBytes       = 20 << 20
)

var labels = map[string]string{"claude": "Claude Code", "codex": "Codex"}

func main() {
	statePath := flag.String("state", "internal/rules/watch-state.json", "last reviewed version per harness")
	outPath := flag.String("out", "release-notes.md", "where to write the review checklist")
	flag.Parse()
	if err := run(*statePath, *outPath); err != nil {
		fmt.Fprintln(os.Stderr, "rulesbot:", err)
		os.Exit(1)
	}
}

func run(statePath, outPath string) error {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	state := map[string]string{}
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%s: %w", statePath, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	changelog, err := fetch(ctx, claudeChangelogURL)
	if err != nil {
		return err
	}
	releasesJSON, err := fetch(ctx, codexReleasesURL)
	if err != nil {
		return err
	}
	var ghReleases []watch.GitHubRelease
	if err := json.Unmarshal(releasesJSON, &ghReleases); err != nil {
		return fmt.Errorf("codex releases: %w", err)
	}

	sources := map[string][]watch.Release{
		"claude": watch.ParseChangelog(string(changelog)),
		"codex":  watch.FromGitHubReleases(ghReleases),
	}
	var all []watch.Candidate
	for _, h := range []string{"claude", "codex"} {
		cands, newest, err := watch.Newer(h, sources[h], state[h])
		if err != nil {
			return err
		}
		all = append(all, cands...)
		state[h] = newest
	}
	if err := os.WriteFile(outPath, []byte(watch.Markdown(all, labels)), 0o644); err != nil {
		return err
	}
	updated, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, append(updated, '\n'), 0o644)
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
}
