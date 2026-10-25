package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Leeminjing/Focus-Go/internal/agentbridge"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest-agent/trees/filecheckpoint"
)

type ArtifactSummary struct {
	Format string `json:"format"`
	Digest string `json:"sha256"`
}
type EvidenceReport struct {
	RunID            string            `json:"run_id"`
	Phase            string            `json:"execution_phase"`
	Sealed           bool              `json:"sealed"`
	RevisionVerified bool              `json:"revision_verified"`
	EvidenceVerified bool              `json:"evidence_verified"`
	ReceiptsComplete bool              `json:"receipts_complete"`
	TestStatus       string            `json:"test_status"`
	Receipts         []domain.Receipt  `json:"receipts"`
	Models           []ArtifactSummary `json:"model_records"`
	Drift            []string          `json:"workspace_drift,omitempty"`
}

func (a *Application) Evidence(ctx context.Context, id string, checkWorkspace bool) (EvidenceReport, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return EvidenceReport{}, err
	}
	report := EvidenceReport{RunID: id, Phase: run.Phase, TestStatus: "not_run", Receipts: []domain.Receipt{}, Models: []ArtifactSummary{}}
	if run.Equipment == nil {
		return report, domain.ErrInvalid
	}
	checkpoint := domain.StableID("checkpoint", id, 0)
	snapshot, err := a.Snapshot(ctx, checkpoint)
	if err == nil {
		if snapshot.Evidence == nil {
			return report, domain.ErrCorrupt
		}
		var record agent.Run
		if err = domain.Strict(snapshot.AgentRecord, &record); err != nil {
			return report, err
		}
		base, err := a.Revision(ctx, run.Base)
		if err != nil {
			return report, err
		}
		if err = agentbridge.Verify(run, base, snapshot); err != nil {
			return report, err
		}
		if err = verifyEvidence(run, base, record, *snapshot.Evidence); err != nil {
			return report, err
		}
		report.Sealed = true
		report.Phase = string(record.Status)
		report.EvidenceVerified = true
		report.ReceiptsComplete = true
		report.Receipts = snapshot.Evidence.Receipts
		for _, artifact := range snapshot.Evidence.ModelRecords {
			report.Models = append(report.Models, ArtifactSummary{artifact.Format, artifact.Digest})
		}
		if run.Settlement != nil && run.Settlement.ContextRevision != nil {
			envelope, err := a.Revision(ctx, *run.Settlement.ContextRevision)
			if err != nil {
				return report, err
			}
			if err = snapshot.Matches(envelope); err != nil {
				return report, err
			}
			report.RevisionVerified = true
		}
	} else if errors.Is(err, domain.ErrNotFound) {
		receipts, err := workspace.LoadReceipts(ctx, filepath.Join(a.directory, "workspace-receipts"), run.AgentRunID)
		if err != nil {
			return report, err
		}
		grant, _ := domain.Hash(run.Equipment.Workspace)
		for _, r := range receipts {
			if r.RunID != id || r.AgentRunID != run.AgentRunID || r.GrantHash != grant || r.Validate() != nil {
				return report, domain.ErrCorrupt
			}
		}
		report.Receipts = receipts
		report.EvidenceVerified = len(receipts) > 0
		// Private Agent availability is diagnostic only; evidence remains readable.
		store, err := filecheckpoint.New(filepath.Join(a.directory, "agent"))
		if err == nil {
			record, err := store.Load(ctx, run.AgentRunID)
			if err == nil {
				report.Phase = string(record.Status)
			}
		}
	} else {
		return report, err
	}
	if checkWorkspace {
		w, err := workspace.New(run.Equipment.Workspace, filepath.Join(a.directory, "workspace-receipts"), nil)
		if err != nil {
			return report, err
		}
		defer w.Close()
		report.Drift, err = w.Drift(ctx, report.Receipts)
		if err != nil {
			return report, err
		}
	}
	return report, nil
}
