// Package config holds seaglass's own files: persisted UI state now, the
// user config file later.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctrl-research/seaglass/internal/k8s"
)

// State is what seaglass remembers between runs.
type State struct {
	LastContext string                  `json:"lastContext,omitempty"`
	Contexts    map[string]ContextState `json:"contexts,omitempty"`
}

// ContextState is the last position within one kube context.
type ContextState struct {
	Namespace     string        `json:"namespace,omitempty"`
	AllNamespaces bool          `json:"allNamespaces,omitempty"`
	Resource      *k8s.Resource `json:"resource,omitempty"`
}

// For returns the saved state for a context, if any.
func (s State) For(context string) (ContextState, bool) {
	cs, ok := s.Contexts[context]
	return cs, ok
}

// Store reads and writes State at a fixed path.
type Store struct{ Path string }

// DefaultStore uses $XDG_STATE_HOME/seaglass/state.json, falling back to
// ~/.local/state/seaglass/state.json.
func DefaultStore() (*Store, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "state.json")}, nil
}

// StateDir returns the seaglass state directory, creating it if needed.
func StateDir() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "seaglass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// Load reads the state. A missing file is an empty state, not an error.
func (s *Store) Load() (State, error) {
	var st State
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	return st, nil
}

// Save writes the state atomically.
func (s *Store) Save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Update records a position for a context and makes it the last context.
func (s *State) Update(context string, cs ContextState) {
	if s.Contexts == nil {
		s.Contexts = map[string]ContextState{}
	}
	s.LastContext = context
	s.Contexts[context] = cs
}
