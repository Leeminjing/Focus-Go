package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

const maxReceiptBytes = domain.MaxDocument

// LoadReceipts reads evidence without opening the workspace, granting file
// access, constructing tools, or requiring the executor to be healthy.
func LoadReceipts(ctx context.Context, directory, agentID string) ([]domain.Receipt, error) {
	path, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	w := &Workspace{directory: path}
	return w.Receipts(ctx, agentID)
}

func (w *Workspace) slot(agentID, id string) string {
	return filepath.Join(w.directory, domain.BytesHash([]byte(agentID)), domain.BytesHash([]byte(id)))
}
func (w *Workspace) load(ctx context.Context, agentID, id string) (domain.Receipt, uint64, error) {
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, 0, err
	}
	entries, err := os.ReadDir(w.slot(agentID, id))
	if err != nil {
		return domain.Receipt{}, 0, err
	}
	names := []string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pending-") {
			continue
		}
		if entry.IsDir() {
			return domain.Receipt{}, 0, domain.ErrCorrupt
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return domain.Receipt{}, 0, os.ErrNotExist
	}
	if len(names) > 2 {
		return domain.Receipt{}, 0, domain.ErrCorrupt
	}
	var result domain.Receipt
	for i, name := range names {
		if name != fmt.Sprintf("%d.json", i+1) {
			return result, 0, domain.ErrCorrupt
		}
		file, err := os.Open(filepath.Join(w.slot(agentID, id), name))
		if err != nil {
			return result, 0, err
		}
		data, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
		closeErr := file.Close()
		if err != nil || closeErr != nil || len(data) > maxReceiptBytes {
			return result, 0, domain.ErrCorrupt
		}
		var next domain.Receipt
		if domain.Strict(data, &next) != nil || next.Validate() != nil || next.ID != id || next.AgentRunID != agentID {
			return result, 0, domain.ErrCorrupt
		}
		if i == 1 {
			if result.Phase != "prepared" || next.Phase == "prepared" || result.ArgumentsHash != next.ArgumentsHash || result.BeforeHash != next.BeforeHash || result.AfterHash != next.AfterHash || result.GrantHash != next.GrantHash || result.RunID != next.RunID {
				return result, 0, domain.ErrCorrupt
			}
		}
		result = next
	}
	return result, uint64(len(names)), nil
}
func (w *Workspace) save(ctx context.Context, r domain.Receipt, sequence uint64) (domain.Receipt, error) {
	r.Digest = ""
	digest, err := domain.Hash(r)
	if err != nil {
		return r, err
	}
	r.Digest = digest
	if err := r.Validate(); err != nil {
		return r, err
	}
	data, err := domain.Marshal(r)
	if err != nil {
		return r, err
	}
	dir := w.slot(r.AgentRunID, r.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return r, err
	}
	file, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return r, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return r, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return r, err
	}
	if err = file.Close(); err != nil {
		return r, err
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	if err = os.Link(file.Name(), filepath.Join(dir, fmt.Sprintf("%d.json", sequence))); err != nil {
		return r, err
	}
	if runtime.GOOS != "windows" {
		f, err := os.Open(dir)
		if err != nil {
			return r, err
		}
		defer f.Close()
		if err = f.Sync(); err != nil {
			return r, err
		}
	}
	return r, nil
}
func (w *Workspace) Receipts(ctx context.Context, agentID string) ([]domain.Receipt, error) {
	dir := filepath.Join(w.directory, domain.BytesHash([]byte(agentID)))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Receipt{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 64 {
		return nil, domain.ErrCorrupt
	}
	result := []domain.Receipt{}
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, domain.ErrCorrupt
		}
		files, err := os.ReadDir(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var id string
		for _, file := range files {
			if file.Name() != "1.json" {
				continue
			}
			f, err := os.Open(filepath.Join(dir, entry.Name(), file.Name()))
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
			f.Close()
			if err != nil || len(data) > maxReceiptBytes {
				return nil, domain.ErrCorrupt
			}
			var r domain.Receipt
			if domain.Strict(data, &r) != nil {
				return nil, domain.ErrCorrupt
			}
			id = r.ID
		}
		if id == "" || domain.BytesHash([]byte(id)) != entry.Name() {
			return nil, domain.ErrCorrupt
		}
		r, _, err := w.load(ctx, agentID, id)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
