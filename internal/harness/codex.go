package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
)

const codex = "codex"

// CodexIdleThreshold is how long an unarchived thread may go without an update
// before tidewake treats it as ended. Codex has no per-thread liveness signal.
const CodexIdleThreshold = 24 * time.Hour

type codexThread struct {
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Archived  int    `json:"archived"`
	UpdatedAt int64  `json:"updated_at"`
	GitBranch string `json:"git_branch"`
}

type codexChatProcess struct {
	OSPid          int    `json:"osPid"`
	ConversationID string `json:"conversationId"`
	StartedAtMs    int64  `json:"startedAtMs"`
}

// codexStateDB returns the ~/.codex/state_<n>.sqlite with the highest n.
func codexStateDB(home string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no state_*.sqlite in %s", home)
	}
	sort.Slice(matches, func(i, j int) bool { return stateGen(matches[i]) < stateGen(matches[j]) })
	return matches[len(matches)-1], nil
}

func stateGen(path string) int {
	base := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "state_"), ".sqlite")
	n, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return n
}

// readCodexThreads reads the undocumented threads table via the sqlite3 CLI,
// opened read-only so a running Codex is never disturbed.
func readCodexThreads(ctx context.Context, st *State, run Runner, home string, codexRunning bool, now time.Time) error {
	db, err := codexStateDB(home)
	if err != nil {
		return err
	}
	out, err := run(ctx, "sqlite3", "-readonly", "-json", db,
		"select id, cwd, archived, updated_at, coalesce(git_branch,'') as git_branch from threads")
	if err != nil {
		return fmt.Errorf("sqlite3 %s: %w", db, err)
	}
	if len(out) == 0 {
		return nil
	}
	var threads []codexThread
	if err := json.Unmarshal(out, &threads); err != nil {
		return fmt.Errorf("codex threads: %w", err)
	}
	for _, t := range threads {
		alive, why := codexThreadAlive(t, codexRunning, now)
		st.add(&Session{Harness: codex, ID: t.ID, Name: t.GitBranch, Cwd: t.Cwd, Alive: alive, Why: why})
	}
	return nil
}

func codexThreadAlive(t codexThread, codexRunning bool, now time.Time) (bool, string) {
	switch {
	case t.Archived != 0:
		return false, "thread is archived"
	case !codexRunning:
		return false, "no Codex process is running"
	}
	idle := now.Sub(time.Unix(t.UpdatedAt, 0))
	if idle > CodexIdleThreshold {
		return false, fmt.Sprintf("thread idle for %dd", int(idle.Hours()/24))
	}
	return true, "thread updated recently and Codex is running"
}

// readCodexChatProcesses reads process_manager/chat_processes.json, the
// processes the Codex app itself manages. The file keeps entries for long-dead
// processes, so an entry counts only if its start time matches the live PID.
func readCodexChatProcesses(st *State, home string, snap *proc.Snapshot) error {
	data, err := os.ReadFile(filepath.Join(home, "process_manager", "chat_processes.json"))
	if err != nil {
		return err
	}
	var procs []codexChatProcess
	if err := json.Unmarshal(data, &procs); err != nil {
		return fmt.Errorf("chat_processes.json: %w", err)
	}
	for _, p := range procs {
		if p.OSPid > 0 && p.StartedAtMs > 0 && snap.AliveSince(p.OSPid, time.UnixMilli(p.StartedAtMs)) {
			st.CodexProcesses[p.OSPid] = p.ConversationID
		}
	}
	return nil
}

// AnyRunning reports whether a process matching match is alive.
func AnyRunning(snap *proc.Snapshot, match func(*proc.Process) bool) bool {
	for _, p := range snap.Procs {
		if !p.Exiting() && match(p) {
			return true
		}
	}
	return false
}
