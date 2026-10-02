// Package rules holds everything that changes between harness releases:
// process signatures, paths and the known-issue table. It is data so the
// release-watch bot can update it without code changes.
package rules

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed pack.json
var packJSON []byte

// Harness describes how to recognise one agent harness.
type Harness struct {
	Label           string   `json:"label"`
	VersionCommand  []string `json:"versionCommand"`
	ProcessNames    []string `json:"processNames"`
	ProcessContains []string `json:"processContains"`
	SessionEnv      string   `json:"sessionEnv"`
	MarkerEnv       []string `json:"markerEnv"`
	AgentPaths      []string `json:"agentPaths"`
	WorktreePaths   []string `json:"worktreePaths"`
	// SessionPathPattern captures a session id from a scratch path, as group 1.
	SessionPathPattern string `json:"sessionPathPattern,omitempty"`
}

// Issue is one known leak or cleanup bug.
type Issue struct {
	Harness string `json:"harness"`
	Status  string `json:"status"` // "open" or "fixed"
	FixedIn string `json:"fixedIn,omitempty"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Covers  string `json:"covers,omitempty"`
}

// Pack is the full rules pack.
type Pack struct {
	Version     string             `json:"version"`
	Harnesses   map[string]Harness `json:"harnesses"`
	KnownIssues []Issue            `json:"knownIssues"`
}

// Load returns the embedded pack.
func Load() (*Pack, error) { return Parse(packJSON) }

// Parse decodes and validates a pack.
func Parse(data []byte) (*Pack, error) {
	var p Pack
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("rules pack: %w", err)
	}
	for name, h := range p.Harnesses {
		if h.SessionPathPattern == "" {
			continue
		}
		re, err := regexp.Compile(h.SessionPathPattern)
		if err != nil || re.NumSubexp() != 1 {
			return nil, fmt.Errorf("rules pack: %s sessionPathPattern must compile with one group", name)
		}
	}
	for i, is := range p.KnownIssues {
		if _, ok := p.Harnesses[is.Harness]; !ok {
			return nil, fmt.Errorf("rules pack: issue %d names unknown harness %q", i, is.Harness)
		}
		if is.Status == "fixed" {
			if _, err := ParseVersion(is.FixedIn); err != nil {
				return nil, fmt.Errorf("rules pack: issue %d: %w", i, err)
			}
		} else if is.Status != "open" {
			return nil, fmt.Errorf("rules pack: issue %d has status %q", i, is.Status)
		}
	}
	return &p, nil
}

// Version is a dotted numeric version such as 2.1.287.
type Version []int

// ParseVersion extracts the first dotted number from s, e.g. "2.1.287 (Claude Code)".
func ParseVersion(s string) (Version, error) {
	for _, tok := range strings.Fields(s) {
		tok = strings.TrimPrefix(strings.TrimPrefix(tok, "rust-"), "v")
		parts := strings.Split(tok, ".")
		if len(parts) < 2 {
			continue
		}
		v := make(Version, 0, len(parts))
		for _, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil {
				v = nil
				break
			}
			v = append(v, n)
		}
		if v != nil {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no version in %q", s)
}

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	for i := 0; i < len(v) || i < len(o); i++ {
		a, b := at(v, i), at(o, i)
		if a != b {
			return a < b
		}
	}
	return false
}

func (v Version) String() string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ".")
}

func at(v Version, i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}

// IsHarnessProcess reports whether command belongs to the harness itself.
func (h Harness) IsHarnessProcess(name, command string) bool {
	for _, n := range h.ProcessNames {
		if name == n {
			return true
		}
	}
	for _, c := range h.ProcessContains {
		if strings.Contains(command, c) {
			return true
		}
	}
	return false
}

// InAgentPath reports whether s points into this harness's scratch or state dirs.
func (h Harness) InAgentPath(s string) bool {
	for _, p := range h.AgentPaths {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
