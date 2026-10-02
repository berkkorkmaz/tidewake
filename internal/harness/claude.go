package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
)

const claude = "claude"

// terminalAgentStates are `claude agents` states of a session that has ended.
// Any state not listed here counts as alive, so a new state never causes a kill.
var terminalAgentStates = map[string]bool{"done": true, "failed": true, "stopped": true}

type claudeSessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	ProcStart string `json:"procStart"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Name      string `json:"name"`
}

type claudeAgent struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Status    string `json:"status"`
	PID       int    `json:"pid"`
}

// readClaudeSessionFiles reads ~/.claude/sessions/<pid>.json (one per running
// session process; undocumented format, so every field is optional).
func readClaudeSessionFiles(st *State, dir string, snap *proc.Snapshot) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		if _, statErr := os.Stat(dir); statErr != nil {
			return statErr
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f claudeSessionFile
		if json.Unmarshal(data, &f) != nil || f.SessionID == "" {
			continue
		}
		// procStart is written in UTC, unlike ps, which prints local time.
		started, _ := time.ParseInLocation(proc.LstartLayout, f.ProcStart, time.UTC)
		alive := snap.AliveSince(f.PID, started)
		why := fmt.Sprintf("session process %d is running", f.PID)
		if !alive {
			why = fmt.Sprintf("session process %d has exited", f.PID)
		}
		st.add(&Session{Harness: claude, ID: f.SessionID, Name: f.Name, Cwd: f.Cwd, Alive: alive, Why: why})
	}
	return nil
}

// readClaudeAgents reads `claude agents --json --all`, the supported listing.
func readClaudeAgents(ctx context.Context, st *State, run Runner, snap *proc.Snapshot) error {
	out, err := run(ctx, "claude", "agents", "--json", "--all")
	if err != nil {
		return err
	}
	var agents []claudeAgent
	if err := json.Unmarshal(out, &agents); err != nil {
		return fmt.Errorf("claude agents --json: %w", err)
	}
	for _, a := range agents {
		if a.SessionID == "" {
			continue
		}
		alive, why := claudeAgentAlive(a, snap)
		st.add(&Session{Harness: claude, ID: a.SessionID, Name: a.Name, Cwd: a.Cwd, Alive: alive, Why: why})
	}
	return nil
}

func claudeAgentAlive(a claudeAgent, snap *proc.Snapshot) (bool, string) {
	if a.State != "" {
		if terminalAgentStates[a.State] {
			return false, fmt.Sprintf("claude agents reports state %q", a.State)
		}
		return true, fmt.Sprintf("claude agents reports state %q", a.State)
	}
	if a.PID > 0 && !snap.Alive(a.PID) {
		return false, fmt.Sprintf("session process %d has exited", a.PID)
	}
	return true, fmt.Sprintf("claude agents reports status %q", a.Status)
}
