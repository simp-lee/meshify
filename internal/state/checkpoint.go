// Package state persists resumable deploy checkpoints and host mutations.
package state

import (
	"encoding/json"
	"fmt"
	"meshify/internal/assets"
	"meshify/internal/workflow"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type LoadErrorKind string

const (
	LoadErrorRead   LoadErrorKind = "read"
	LoadErrorDecode LoadErrorKind = "decode"
)

type LoadError struct {
	Path string
	Kind LoadErrorKind
	Err  error
}

func (err *LoadError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%s checkpoint: %v", err.Kind, err.Err)
}

func (err *LoadError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type Checkpoint struct {
	DesiredStateDigest   string                    `json:"desired_state_digest,omitempty"`
	CurrentCheckpoint    string                    `json:"current_checkpoint,omitempty"`
	CompletedCheckpoints []string                  `json:"completed_checkpoints,omitempty"`
	ModifiedPaths        []string                  `json:"modified_paths,omitempty"`
	ActivationHistory    []assets.Activation       `json:"activation_history,omitempty"`
	LastFailure          *workflow.FailureSnapshot `json:"last_failure,omitempty"`
	UpdatedAt            time.Time                 `json:"updated_at,omitempty"`
}

func (checkpoint Checkpoint) HasDeployContext() bool {
	return strings.TrimSpace(checkpoint.CurrentCheckpoint) != "" ||
		len(checkpoint.CompletedCheckpoints) > 0 ||
		len(checkpoint.ModifiedPaths) > 0 ||
		len(checkpoint.ActivationHistory) > 0 ||
		checkpoint.LastFailure != nil
}

func (checkpoint Checkpoint) MatchesDesiredState(desiredStateDigest string) bool {
	return strings.TrimSpace(checkpoint.DesiredStateDigest) == strings.TrimSpace(desiredStateDigest)
}

func (checkpoint *Checkpoint) BeginDeploy(desiredStateDigest string) bool {
	trimmedDigest := strings.TrimSpace(desiredStateDigest)
	changed := false

	if checkpoint.DesiredStateDigest != trimmedDigest {
		checkpoint.DesiredStateDigest = trimmedDigest
		changed = checkpoint.resetDeployState() || changed
	} else if checkpoint.CurrentCheckpoint == "" && checkpoint.LastFailure == nil {
		if len(checkpoint.CompletedCheckpoints) > 0 {
			checkpoint.CompletedCheckpoints = nil
			changed = true
		}
		if len(checkpoint.ModifiedPaths) > 0 {
			checkpoint.ModifiedPaths = nil
			changed = true
		}
		if len(checkpoint.ActivationHistory) > 0 {
			checkpoint.ActivationHistory = nil
			changed = true
		}
	}

	if changed {
		checkpoint.touch()
	}
	return changed
}

func (checkpoint *Checkpoint) FinalizeSuccessfulDeploy() bool {
	changed := false
	if checkpoint.CurrentCheckpoint != "" {
		checkpoint.CurrentCheckpoint = ""
		changed = true
	}
	if checkpoint.LastFailure != nil {
		checkpoint.LastFailure = nil
		changed = true
	}
	if changed {
		checkpoint.touch()
	}
	return changed
}

func (checkpoint *Checkpoint) MarkCompleted(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	checkpoint.CurrentCheckpoint = trimmed
	if slices.Contains(checkpoint.CompletedCheckpoints, trimmed) {
		checkpoint.touch()
		return false
	}
	checkpoint.CompletedCheckpoints = append(checkpoint.CompletedCheckpoints, trimmed)
	checkpoint.touch()
	return true
}

func (checkpoint *Checkpoint) RecordModifiedPaths(paths ...string) bool {
	changed := false
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if containsString(checkpoint.ModifiedPaths, trimmed) {
			continue
		}
		checkpoint.ModifiedPaths = append(checkpoint.ModifiedPaths, trimmed)
		changed = true
	}
	if changed {
		checkpoint.touch()
	}
	return changed
}

func (checkpoint *Checkpoint) RecordActivations(activations ...assets.Activation) bool {
	changed := false
	for _, activation := range activations {
		if activation == "" {
			continue
		}
		if containsActivation(checkpoint.ActivationHistory, activation) {
			continue
		}
		checkpoint.ActivationHistory = append(checkpoint.ActivationHistory, activation)
		changed = true
	}
	if changed {
		checkpoint.touch()
	}
	return changed
}

func (checkpoint Checkpoint) HasCompleted(name string) bool {
	trimmed := strings.TrimSpace(name)
	return containsString(checkpoint.CompletedCheckpoints, trimmed)
}

func (checkpoint *Checkpoint) RecordFailure(failure workflow.FailureSnapshot) bool {
	if !failure.HasContent() {
		if checkpoint.LastFailure == nil {
			return false
		}
		checkpoint.LastFailure = nil
		checkpoint.touch()
		return true
	}

	snapshot := failure
	snapshot.Remediation = append([]string(nil), failure.Remediation...)
	checkpoint.LastFailure = &snapshot
	checkpoint.touch()
	return true
}

type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: strings.TrimSpace(path)}
}

func (store Store) Load() (Checkpoint, error) {
	if store.path == "" {
		return Checkpoint{}, fmt.Errorf("checkpoint path is required")
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, nil
		}
		return Checkpoint{}, &LoadError{Path: store.path, Kind: LoadErrorRead, Err: err}
	}

	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return Checkpoint{}, &LoadError{Path: store.path, Kind: LoadErrorDecode, Err: err}
	}
	return checkpoint, nil
}

func (store Store) Save(checkpoint Checkpoint) error {
	if store.path == "" {
		return fmt.Errorf("checkpoint path is required")
	}
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}

	file, err := os.CreateTemp(filepath.Dir(store.path), filepath.Base(store.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	temporaryPath := file.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("replace checkpoint: %w", err)
	}
	return nil
}

func (checkpoint *Checkpoint) touch() {
	checkpoint.UpdatedAt = time.Now().UTC()
}

func (checkpoint *Checkpoint) resetDeployState() bool {
	changed := false
	if checkpoint.CurrentCheckpoint != "" {
		checkpoint.CurrentCheckpoint = ""
		changed = true
	}
	if len(checkpoint.CompletedCheckpoints) > 0 {
		checkpoint.CompletedCheckpoints = nil
		changed = true
	}
	if len(checkpoint.ModifiedPaths) > 0 {
		checkpoint.ModifiedPaths = nil
		changed = true
	}
	if len(checkpoint.ActivationHistory) > 0 {
		checkpoint.ActivationHistory = nil
		changed = true
	}
	if checkpoint.LastFailure != nil {
		checkpoint.LastFailure = nil
		changed = true
	}
	return changed
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func containsActivation(values []assets.Activation, want assets.Activation) bool {
	return slices.Contains(values, want)
}
