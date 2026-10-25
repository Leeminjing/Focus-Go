package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/store"
)

func TestLatestCatalogOrReferencedSnapshotCorruptionDoesNotFallBack(t *testing.T) {
	for _, target := range []string{"catalog", "snapshots"} {
		t.Run(target, func(t *testing.T) {
			directory := t.TempDir()
			repository, err := store.New(directory)
			if err != nil {
				t.Fatal(err)
			}
			f := fixture(t)
			ctx := context.Background()
			if err := repository.Install(ctx, f, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Admit(ctx, domain.Request{WorkspaceID: "w1", ContextID: "c1", Expected: f.Context.Current, Input: "ADD=2", Key: "one"}, "fixture-base-add-v1"); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(directory, target))
			if err != nil {
				t.Fatal(err)
			}
			var latest string
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".json" && entry.Name() > latest {
					latest = entry.Name()
				}
			}
			if latest == "" {
				t.Fatal("no committed file")
			}
			if err := os.WriteFile(filepath.Join(directory, target, latest), []byte(`{"corrupt":`), 0600); err != nil {
				t.Fatal(err)
			}
			if target == "catalog" {
				_, err = repository.State(ctx)
			} else {
				_, err = repository.Revision(ctx, f.Context.Current)
			}
			if !errors.Is(err, domain.ErrCorrupt) {
				t.Fatal("silently used older state", err)
			}
		})
	}
}
