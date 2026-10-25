package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/store"
)

func fixture(t *testing.T) domain.Fixture {
	t.Helper()
	data, err := os.ReadFile("../../testdata/base-40.json")
	if err != nil {
		t.Fatal(err)
	}
	var f domain.Fixture
	if err := domain.Strict(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRepositoryBindsExactRevisionAndAdmitsOneMainRun(t *testing.T) {
	r, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := fixture(t)
	if err := r.Install(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	ref := f.Context.Current
	bad := ref
	bad.Generation++
	if _, err := r.Revision(ctx, bad); !errors.Is(err, domain.ErrRefMismatch) {
		t.Fatal(err)
	}
	var won, busy atomic.Int32
	var wg sync.WaitGroup
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Admit(ctx, domain.Request{WorkspaceID: "w1", ContextID: "c1", Expected: ref, Input: "ADD=2", Key: key}, "fixture-base-add-v1")
			if err == nil {
				won.Add(1)
			} else if errors.Is(err, domain.ErrBusy) {
				busy.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || busy.Load() != 1 {
		t.Fatal(won.Load(), busy.Load())
	}
	state, err := r.State(ctx)
	if err != nil || len(state.Runs) != 1 {
		t.Fatal(state, err)
	}
	for _, run := range state.Runs {
		req := domain.Request{WorkspaceID: "w1", ContextID: "c1", Expected: ref, Input: "ADD=2", Key: run.Key}
		same, err := r.Admit(ctx, req, "fixture-base-add-v1")
		if err != nil || same.ID != run.ID {
			t.Fatal(same, err)
		}
		req.Input = "different"
		if _, err := r.Admit(ctx, req, "fixture-base-add-v1"); !errors.Is(err, domain.ErrIdempotency) {
			t.Fatal(err)
		}
	}
}
