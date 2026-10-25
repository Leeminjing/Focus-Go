package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
)

func TestCodingApprovalIsBoundToPreviewAndAgentRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	original := "package calc\nfunc Add(a,b int)int{return a-b}\n"
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w1", root, []string{"calc.go"}, []string{"calc.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := app.CodingEquipment(binding, "approval-model")
	if err != nil {
		t.Fatal(err)
	}
	a := open(t, app.Options{Coding: &equipment, ModelProfile: equipment.ModelProfile, Model: modelFunc(codingProcessModel)})
	data, f := load(t)
	if err = a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	paused, err := a.Run(ctx, request(f, "approval-coding"))
	if err != nil || paused.Run.Settlement != nil {
		t.Fatal(paused, err)
	}
	record, err := a.AgentState(ctx, paused.Run.ID)
	if err != nil || record.Status != agent.AwaitingApproval {
		t.Fatal(record, err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(actual) != original {
		t.Fatal("write preceded approval")
	}
	preview, err := a.Preview(ctx, paused.Run.ID, record.Revision, "edit")
	if err != nil || preview["old_text"] != "a-b" || preview["new_text"] != "a+b" {
		t.Fatal(preview, err)
	}
	if _, err = a.Decide(ctx, paused.Run.ID, record.Revision-1, "edit", agent.Granted); !errors.Is(err, agent.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = a.Decide(ctx, paused.Run.ID, record.Revision, "edit", agent.Granted); err != nil {
		t.Fatal(err)
	}
	result, err := a.Resume(ctx, paused.Run.ID)
	if err != nil || !result.Verified {
		t.Fatal(result, err)
	}
	actual, _ = os.ReadFile(filepath.Join(root, "calc.go"))
	if string(actual) == original {
		t.Fatal("approved write missing")
	}
}
