package workflowstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Directory names under the repository root.
const (
	ActiveDir    = "active"
	CompletedDir = "completed"
)

// FileRepository stores one JSON file per workflow run.
//
// Runs that may still execute live in active/; runs in a final state are
// moved to completed/ so that recovery only scans what matters. Every write
// goes to a temporary file that is fsynced and then atomically renamed over
// the target, so a crash mid-write leaves the previous version intact.
type FileRepository struct {
	root string
}

// NewFileRepository prepares root and its subdirectories.
func NewFileRepository(root string) (*FileRepository, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("workflowstate: root directory is required")
	}
	for _, dir := range []string{ActiveDir, CompletedDir} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			return nil, fmt.Errorf("workflowstate: create %s: %w", dir, err)
		}
	}
	return &FileRepository{root: root}, nil
}

// Get implements workflow.Repository.
func (r *FileRepository) Get(_ context.Context, correlationID string) (*workflow.State, error) {
	if !correlation.IsValid(correlationID) {
		return nil, fmt.Errorf("workflowstate: invalid correlation id %q", correlationID)
	}
	for _, dir := range []string{ActiveDir, CompletedDir} {
		data, err := os.ReadFile(r.path(dir, correlationID))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("workflowstate: read %s: %w", correlationID, err)
		}
		var st workflow.State
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("workflowstate: decode %s: %w", correlationID, err)
		}
		return &st, nil
	}
	return nil, workflow.ErrStateNotFound
}

// Save implements workflow.Repository.
func (r *FileRepository) Save(_ context.Context, state *workflow.State) error {
	if state == nil || !correlation.IsValid(state.CorrelationID) {
		return errors.New("workflowstate: state needs a valid correlation id")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("workflowstate: encode %s: %w", state.CorrelationID, err)
	}

	dir := ActiveDir
	if state.Status.IsFinal() {
		dir = CompletedDir
	}
	if err := writeAtomically(r.path(dir, state.CorrelationID), data); err != nil {
		return fmt.Errorf("workflowstate: write %s: %w", state.CorrelationID, err)
	}
	if dir == CompletedDir {
		if err := os.Remove(r.path(ActiveDir, state.CorrelationID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workflowstate: remove active copy of %s: %w", state.CorrelationID, err)
		}
	}
	return nil
}

// ListActive implements workflow.Repository.
func (r *FileRepository) ListActive(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(r.root, ActiveDir))
	if err != nil {
		return nil, fmt.Errorf("workflowstate: list active: %w", err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	return ids, nil
}

func (r *FileRepository) path(dir, correlationID string) string {
	return filepath.Join(r.root, dir, correlationID+".json")
}

// writeAtomically writes data to a temporary file in the target directory,
// fsyncs it and renames it over path.
func writeAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
