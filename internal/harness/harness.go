// Package harness reads Claude Code and Codex session state and decides
// which sessions are still alive.
package harness

import (
	"context"
	"os/exec"
	"time"
)

// Session is one agent session known to a harness.
type Session struct {
	Harness string `json:"harness"`
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Alive   bool   `json:"alive"`
	// Why explains the liveness verdict in one phrase, shown as proof.
	Why string `json:"why"`
}

// Key identifies a session across harnesses.
func Key(harness, id string) string { return harness + ":" + id }

// Runner executes a command and returns its stdout; tests supply fakes.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs real commands with a timeout.
func ExecRunner(timeout time.Duration) Runner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Output()
	}
}

// State is everything tidewake learned about sessions.
type State struct {
	Sessions map[string]*Session
	// Sources lists which state sources were readable, for doctor.
	Sources map[string]error
	// CodexProcesses maps OS pid to Codex conversation for managed processes.
	CodexProcesses map[int]string
}

// Lookup returns the session or nil.
func (s *State) Lookup(harness, id string) *Session { return s.Sessions[Key(harness, id)] }

func (s *State) add(sess *Session) {
	k := Key(sess.Harness, sess.ID)
	if prev, ok := s.Sessions[k]; ok && prev.Alive && !sess.Alive {
		// Any source that sees the session alive wins: false "dead" is the costly error.
		return
	}
	s.Sessions[k] = sess
}
