package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/store"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
)

func TestFrozenCodingEquipmentExcludesAnotherContextInSameWorkspace(t *testing.T) {
	ctx := context.Background()
	repo, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w1", t.TempDir(), []string{"."}, []string{"a.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.RegisterWorkspace(ctx, binding); err != nil {
		t.Fatal(err)
	}
	equipment := domain.Equipment{Version: domain.CodingVersion, Workspace: binding, ModelProfile: "test-model", Tools: []domain.ToolDescriptor{}}
	for _, name := range []string{"workspace_search", "workspace_read", "workspace_edit"} {
		spec, _ := workspace.Schema(name, true)
		equipment.Tools = append(equipment.Tools, domain.ToolDescriptor{Name: spec.Name, Version: spec.Version, Description: spec.Description, Parameters: spec.Parameters, NeedsApproval: spec.NeedsApproval})
	}
	f := fixture(t)
	if err = repo.Install(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	request := domain.Request{WorkspaceID: "w1", ContextID: "c1", Expected: f.Context.Current, Input: "fix", Key: "coding", Equipment: &equipment}
	run, err := repo.Admit(ctx, request, "test-model")
	if err != nil || run.Equipment == nil || run.Version != domain.CodingVersion {
		t.Fatal(run, err)
	}
	f.Context.ID = "c2"
	f.Context.Current.ContextID = "c2"
	f.Context.Current.RevisionID = "r-other"
	f.Context.Current.CheckpointID = domain.Pointer("cp-other")
	f.Revision.Revision.Ref = f.Context.Current
	f.Revision.Revision.ContentHash, _ = f.Revision.ExpectedHash()
	if err = repo.Install(ctx, f, nil); err != nil {
		t.Fatal(err)
	}
	request.ContextID = "c2"
	request.Expected = f.Context.Current
	request.Key = "competing"
	if _, err = repo.Admit(ctx, request, "test-model"); !errors.Is(err, domain.ErrBusy) {
		t.Fatal("competing code writer admitted", err)
	}
	state, err := repo.State(ctx)
	if err != nil || state.Version != domain.CodingVersion || len(state.Runs) != 1 {
		t.Fatal(state, err)
	}
}
