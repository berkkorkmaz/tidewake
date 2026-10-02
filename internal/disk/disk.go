// Package disk reports disk used by agent harnesses and Docker.
package disk

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/berkkorkmaz/tidewake/internal/harness"
	"github.com/berkkorkmaz/tidewake/internal/shell"
)

// Item is one line of the disk report.
type Item struct {
	Category    string `json:"category"`
	Path        string `json:"path,omitempty"`
	Bytes       int64  `json:"bytes"`
	Reclaimable int64  `json:"reclaimableBytes"`
	Suggest     string `json:"suggest,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Docker prune suggestions keep a week of history and a build-cache budget
// instead of wiping everything.
const (
	dockerKeepFor     = "168h"
	dockerCacheBudget = "10gb"
)

// CodexPackages finds Codex releases other than the one `current` points to.
// A release a running process was started from is never suggested.
// runningPaths holds running command lines and resolved executable paths,
// since a process started through `current/` does not name its release.
func CodexPackages(codexHome string, runningPaths []string) []Item {
	pkgs, err := filepath.Glob(filepath.Join(codexHome, "packages", "*"))
	if err != nil {
		return nil
	}
	var items []Item
	for _, pkg := range pkgs {
		current, _ := filepath.EvalSymlinks(filepath.Join(pkg, "current"))
		releases, _ := filepath.Glob(filepath.Join(pkg, "releases", "*"))
		for _, rel := range releases {
			if resolved, _ := filepath.EvalSymlinks(rel); resolved == current || inUse(rel, runningPaths) {
				continue
			}
			size := DirSize(rel)
			items = append(items, Item{
				Category: "codex old release", Path: rel, Bytes: size, Reclaimable: size,
				Suggest: "trash " + shell.Quote(rel),
				Note:    "not the current release and no running process uses it",
			})
		}
	}
	return items
}

func inUse(dir string, paths []string) bool {
	for _, c := range paths {
		if strings.Contains(c, dir+"/") {
			return true
		}
	}
	return false
}

// HarnessStores reports transcript and log stores; informational only.
func HarnessStores(claudeHome, codexHome string) []Item {
	stores := []struct{ category, path, note string }{
		{"claude transcripts", filepath.Join(claudeHome, "projects"), "pruned by Claude Code after cleanupPeriodDays"},
		{"codex sessions", filepath.Join(codexHome, "sessions"), "Codex has no retention setting; archive old threads in Codex"},
		{"codex archived sessions", filepath.Join(codexHome, "archived_sessions"), ""},
	}
	var items []Item
	for _, s := range stores {
		if _, err := os.Stat(s.path); err != nil {
			continue
		}
		items = append(items, Item{Category: s.category, Path: s.path, Bytes: DirSize(s.path), Note: s.note})
	}
	return items
}

type dockerDFRow struct {
	Type        string `json:"Type"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

// Docker reads `docker system df`. A missing or stopped Docker yields no items.
func Docker(ctx context.Context, run harness.Runner) ([]Item, error) {
	out, err := run(ctx, "docker", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return ParseDockerDF(out), nil
}

// ParseDockerDF parses `docker system df --format '{{json .}}'`.
func ParseDockerDF(out []byte) []Item {
	var items []Item
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var row dockerDFRow
		if json.Unmarshal([]byte(sc.Text()), &row) != nil {
			continue
		}
		size, _ := ParseSize(row.Size)
		reclaim, _ := ParseSize(strings.Fields(row.Reclaimable + " ")[0])
		item := Item{Category: "docker " + strings.ToLower(row.Type), Bytes: size, Reclaimable: reclaim}
		switch row.Type {
		case "Images":
			item.Suggest = "docker image prune -a --filter until=" + dockerKeepFor
			item.Note = "Docker's figure assumes every unused image goes; the week filter frees less"
		case "Containers":
			// A stopped container's writable layer can hold data, so this stays a review item.
			item.Reclaimable = 0
			item.Note = "stopped containers keep their writable layer; review with `docker ps -a -f status=exited`"
		case "Build Cache":
			item.Suggest = "docker builder prune --max-used-space " + dockerCacheBudget
		case "Local Volumes":
			item.Reclaimable = 0
			item.Note = "volumes may hold data; review with `docker volume ls -f dangling=true`"
		}
		items = append(items, item)
	}
	return items
}

var sizeUnits = map[string]float64{
	"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
}

// ParseSize parses Docker's human sizes such as "14.22GB" or "0B".
func ParseSize(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	units := make([]string, 0, len(sizeUnits))
	for u := range sizeUnits {
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool { return len(units[i]) > len(units[j]) })
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u); ok {
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, false
			}
			return int64(f * sizeUnits[u]), true
		}
	}
	return 0, false
}

// DirSize sums file sizes under root without following symlinks.
func DirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
