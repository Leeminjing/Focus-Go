// Package store persists the Focus domain independently of Agent checkpoints.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

type Repository struct{ directory string }
type catalogFrame struct {
	Version  int            `json:"version"`
	Sequence uint64         `json:"sequence"`
	Digest   string         `json:"sha256"`
	Catalog  domain.Catalog `json:"catalog"`
}
type snapshotFrame struct {
	Version  int             `json:"version"`
	Digest   string          `json:"sha256"`
	Snapshot domain.Snapshot `json:"snapshot"`
}

func New(directory string) (*Repository, error) {
	if directory == "" {
		return nil, domain.ErrInvalid
	}
	path, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"catalog", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(path, name), 0700); err != nil {
			return nil, err
		}
	}
	return &Repository{directory: path}, nil
}
func read(ctx context.Context, path string, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxDocument+1))
	if err != nil {
		return err
	}
	if err := domain.Strict(data, target); err != nil {
		return errors.Join(domain.ErrCorrupt, err)
	}
	return ctx.Err()
}

// publish never overwrites a committed file. Unsupported hard links fail;
// there is no non-atomic fallback. An error after link may be indeterminate.
func publish(ctx context.Context, path string, value any) error {
	data, err := domain.Marshal(value)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return domain.ErrConflict
		}
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer directory.Close()
		if err := directory.Sync(); err != nil {
			return err
		}
	}
	return nil
}
func (r *Repository) load(ctx context.Context) (domain.Catalog, uint64, error) {
	if err := ctx.Err(); err != nil {
		return domain.Catalog{}, 0, err
	}
	entries, err := os.ReadDir(filepath.Join(r.directory, "catalog"))
	if err != nil {
		return domain.Catalog{}, 0, err
	}
	var sequence uint64
	var name string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		number, err := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if err != nil || number == 0 || number > uint64(domain.MaxInteger) || fmt.Sprintf("%020d.json", number) != entry.Name() || entry.IsDir() {
			return domain.Catalog{}, 0, domain.ErrCorrupt
		}
		if number > sequence {
			sequence = number
			name = entry.Name()
		}
	}
	if sequence == 0 {
		return domain.EmptyCatalog(), 0, nil
	}
	var frame catalogFrame
	if err := read(ctx, filepath.Join(r.directory, "catalog", name), &frame); err != nil {
		return domain.Catalog{}, 0, err
	}
	digest, err := domain.Hash(frame.Catalog)
	if err != nil || digest != frame.Digest || frame.Version != 1 || frame.Sequence != sequence {
		return domain.Catalog{}, 0, domain.ErrCorrupt
	}
	if err := frame.Catalog.Validate(); err != nil {
		return domain.Catalog{}, 0, errors.Join(domain.ErrCorrupt, err)
	}
	return frame.Catalog, sequence, nil
}
func (r *Repository) State(ctx context.Context) (domain.Catalog, error) {
	catalog, _, err := r.load(ctx)
	return catalog, err
}
func (r *Repository) update(ctx context.Context, change func(*domain.Catalog) (bool, error)) error {
	for attempt := 0; attempt < 32; attempt++ {
		old, sequence, err := r.load(ctx)
		if err != nil {
			return err
		}
		next, err := domain.Copy(old)
		if err != nil {
			return err
		}
		changed, err := change(&next)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if sequence >= uint64(domain.MaxInteger) {
			return domain.ErrInvalid
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := immutable(old, next); err != nil {
			return err
		}
		digest, err := domain.Hash(next)
		if err != nil {
			return err
		}
		err = publish(ctx, filepath.Join(r.directory, "catalog", fmt.Sprintf("%020d.json", sequence+1)), catalogFrame{Version: 1, Sequence: sequence + 1, Digest: digest, Catalog: next})
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		return err
	}
	return domain.ErrConflict
}
func immutable(old, next domain.Catalog) error {
	for id, previous := range old.Revisions {
		current, ok := next.Revisions[id]
		a, _ := domain.Hash(previous)
		b, _ := domain.Hash(current)
		if !ok || a != b {
			return domain.ErrConflict
		}
	}
	for id, previous := range old.Contexts {
		current, ok := next.Contexts[id]
		if !ok || previous.WorkspaceID != current.WorkspaceID || previous.ID != current.ID || previous.Lifecycle != current.Lifecycle {
			return domain.ErrConflict
		}
	}
	for id, previous := range old.Runs {
		current, ok := next.Runs[id]
		if !ok || previous.RequestHash != current.RequestHash || previous.AgentRunID != current.AgentRunID || !previous.Base.Equal(current.Base) {
			return domain.ErrConflict
		}
		if previous.Settlement != nil {
			a, _ := domain.Hash(previous)
			b, _ := domain.Hash(current)
			if a != b {
				return domain.ErrConflict
			}
		}
	}
	for id, previous := range old.Events {
		current, ok := next.Events[id]
		a, _ := domain.Hash(previous)
		b, _ := domain.Hash(current)
		if !ok || a != b {
			return domain.ErrConflict
		}
	}
	return nil
}
func (r *Repository) snapshotPath(id string) string {
	return filepath.Join(r.directory, "snapshots", domain.StableID("snapshot", id, 0)+".json")
}
func (r *Repository) Snapshot(ctx context.Context, id string) (domain.Snapshot, error) {
	if !domain.ID(id) {
		return domain.Snapshot{}, domain.ErrInvalid
	}
	var frame snapshotFrame
	if err := read(ctx, r.snapshotPath(id), &frame); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Snapshot{}, domain.ErrNotFound
		}
		return domain.Snapshot{}, err
	}
	digest, err := domain.Hash(frame.Snapshot)
	if err != nil || digest != frame.Digest || frame.Version != 1 || frame.Snapshot.CheckpointID != id {
		return domain.Snapshot{}, domain.ErrCorrupt
	}
	if err := frame.Snapshot.Validate(); err != nil {
		return domain.Snapshot{}, errors.Join(domain.ErrCorrupt, err)
	}
	return frame.Snapshot, nil
}
func (r *Repository) Seal(ctx context.Context, snapshot domain.Snapshot) (domain.Snapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, err
	}
	if existing, err := r.Snapshot(ctx, snapshot.CheckpointID); err == nil {
		return matchSnapshot(existing, snapshot)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Snapshot{}, err
	}
	digest, err := domain.Hash(snapshot)
	if err != nil {
		return domain.Snapshot{}, err
	}
	err = publish(ctx, r.snapshotPath(snapshot.CheckpointID), snapshotFrame{Version: 1, Digest: digest, Snapshot: snapshot})
	if errors.Is(err, domain.ErrConflict) {
		existing, err := r.Snapshot(ctx, snapshot.CheckpointID)
		if err != nil {
			return domain.Snapshot{}, err
		}
		return matchSnapshot(existing, snapshot)
	}
	if err != nil {
		return domain.Snapshot{}, err
	}
	return domain.Copy(snapshot)
}
func matchSnapshot(old, next domain.Snapshot) (domain.Snapshot, error) {
	if old.CheckpointID != next.CheckpointID || old.RunID != next.RunID || old.ContextID != next.ContextID || old.Backend != next.Backend || old.ExecutionThreadID != next.ExecutionThreadID || old.CheckpointNS != next.CheckpointNS || old.HistoryHash != next.HistoryHash || (old.Base == nil) != (next.Base == nil) || (old.Base != nil && !old.Base.Equal(*next.Base)) {
		return domain.Snapshot{}, domain.ErrExecutionIdentity
	}
	return old, nil
}
