package app_test

import (
	"context"
	"errors"
	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"os"
	"path/filepath"
	"testing"
)

func TestUnknownFileEvidenceRemainsReadableWithoutWorkspaceOrCredentials(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte("package calc\nfunc Add(a,b int)int{return a-b}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w1", root, []string{"calc.go"}, []string{"calc.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := app.CodingEquipment(binding, "unknown-evidence")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	fault := errors.New("receipt submission interrupted")
	a := open(t, app.Options{Directory: directory, Coding: &equipment, ModelProfile: equipment.ModelProfile, Model: modelFunc(codingProcessModel), WorkspaceObserver: func(stage workspace.Stage, r domain.Receipt) error {
		if stage == workspace.FileReplaced {
			return fault
		}
		return nil
	}})
	data, f := load(t)
	if err = a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(ctx, request(f, "unknown-evidence"))
	if err == nil || result.Run.Settlement != nil {
		t.Fatal(result, err)
	}
	if err = a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, root+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(root+"-unavailable", root)
	reader, err := app.OpenReader(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	report, err := reader.Evidence(ctx, result.Run.ID, false)
	if err != nil || report.Sealed || report.ReceiptsComplete || !report.EvidenceVerified || report.Phase != "outcome-unknown" || len(report.Receipts) != 2 {
		t.Fatal(report, err)
	}
	found := false
	for _, r := range report.Receipts {
		if r.Tool == "workspace_edit" {
			found = true
			if r.Phase != "prepared" || r.BeforeHash == "" || r.AfterHash == "" || r.Before == r.After || r.Authority == "" {
				t.Fatal("lost prepared evidence", r)
			}
		}
	}
	if !found {
		t.Fatal("prepared edit hidden")
	}
}
