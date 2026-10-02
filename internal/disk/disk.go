// Package disk reports disk used by agent harnesses and Docker.
package disk

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

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

// minScratchBytes hides near-empty scratch folders from the report.
const minScratchBytes = 1 << 20

// SessionState answers whether a Claude session ended, with tidewake's usual
// rule: unknown ids count as ended only when session state was readable.
type SessionState func(id string) (ended bool, why string)

// sessionDirPattern matches a Claude Code session id folder name.
var sessionDirPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ClaudeScratch lists per-session scratch folders under root
// (/private/tmp/claude-<uid>/<project>/<session-id>). Folders of ended
// sessions that no process uses are reclaimable.
func ClaudeScratch(root string, state SessionState, inUse func(dir string) bool) []Item {
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "*"))
	var items []Item
	for _, dir := range dirs {
		id := filepath.Base(dir)
		if !sessionDirPattern.MatchString(id) {
			continue
		}
		size := DirSize(dir)
		if size < minScratchBytes {
			continue
		}
		ended, why := state(id)
		item := Item{Category: "claude scratch", Path: dir, Bytes: size}
		switch {
		case !ended:
			item.Note = "session " + id[:8] + " is still running"
		case inUse(dir):
			item.Note = "session " + id[:8] + " ended, but a process still uses this folder"
		default:
			item.Reclaimable = size
			item.Note = "session " + id[:8] + " ended: " + why
			item.Suggest = "trash " + shell.Quote(dir)
		}
		items = append(items, item)
	}
	return items
}

// HarnessStores reports transcript and log stores; informational only.
func HarnessStores(claudeHome, codexHome string) []Item {
	stores := []struct{ category, path, note string }{
		{"claude transcripts", filepath.Join(claudeHome, "projects"), "pruned by Claude Code after cleanupPeriodDays"},
		{"codex sessions", filepath.Join(codexHome, "sessions"), "Codex has no retention setting; archive old threads in Codex"},
		{"codex archived sessions", filepath.Join(codexHome, "archived_sessions"), ""},
		{"codex temp", filepath.Join(codexHome, ".tmp"), ""},
	}
	for _, pattern := range []string{"thread_history_*.sqlite", "logs_*.sqlite"} {
		matches, _ := filepath.Glob(filepath.Join(codexHome, pattern))
		for _, m := range matches {
			stores = append(stores, struct{ category, path, note string }{"codex " + strings.SplitN(filepath.Base(m), "_", 2)[0] + " db", m, "used by Codex; shown for size only"})
		}
	}
	var items []Item
	for _, s := range stores {
		if _, err := os.Stat(s.path); err != nil {
			continue
		}
		size := DirSize(s.path)
		if info, err := os.Stat(s.path); err == nil && !info.IsDir() {
			size = info.Size()
		}
		items = append(items, Item{Category: s.category, Path: s.path, Bytes: size, Note: s.note})
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

// sizeWorkers bounds the goroutines walking directories in parallel; scratch
// areas can hold half a million files, and a serial walk took 15 s.
const sizeWorkers = 16

// DirSize sums file sizes under root without following symlinks.
func DirSize(root string) int64 {
	var total atomic.Int64
	var wg sync.WaitGroup
	slots := make(chan struct{}, sizeWorkers)
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if e.IsDir() {
				select {
				case slots <- struct{}{}:
					wg.Add(1)
					go func() {
						defer wg.Done()
						walk(path)
						<-slots
					}()
				default:
					walk(path)
				}
				continue
			}
			if info, err := e.Info(); err == nil {
				total.Add(info.Size())
			}
		}
	}
	if info, err := os.Lstat(root); err == nil && !info.IsDir() {
		return info.Size()
	}
	walk(root)
	wg.Wait()
	return total.Load()
}
