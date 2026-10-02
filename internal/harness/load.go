package harness

import (
	"context"
	"path/filepath"
	"time"

	"github.com/berkkorkmaz/tidewake/internal/proc"
	"github.com/berkkorkmaz/tidewake/internal/rules"
)

// Options locates harness state; tests point these at fixtures.
type Options struct {
	ClaudeHome string // usually ~/.claude
	CodexHome  string // usually ~/.codex
	Run        Runner
	Now        time.Time
}

// Load reads every available state source. A missing source is recorded in
// Sources rather than failing, because most users run only one harness.
func Load(ctx context.Context, opts Options, snap *proc.Snapshot, pack *rules.Pack) *State {
	st := &State{
		Sessions:       map[string]*Session{},
		Sources:        map[string]error{},
		CodexProcesses: map[int]string{},
	}
	st.Sources["claude sessions dir"] = readClaudeSessionFiles(st, filepath.Join(opts.ClaudeHome, "sessions"), snap)
	st.Sources["claude agents --json"] = readClaudeAgents(ctx, st, opts.Run, snap)

	codexH := pack.Harnesses[codex]
	running := AnyRunning(snap, func(p *proc.Process) bool { return codexH.IsHarnessProcess(p.Name(), p.Command) })
	st.Sources["codex threads db"] = readCodexThreads(ctx, st, opts.Run, opts.CodexHome, running, opts.Now)
	st.Sources["codex chat_processes.json"] = readCodexChatProcesses(st, opts.CodexHome, snap)
	return st
}
