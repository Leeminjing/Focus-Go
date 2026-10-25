package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func (r *Repository) Install(ctx context.Context, fixture domain.Fixture, expected *domain.Ref) error {
	if err := fixture.Validate(); err != nil {
		return err
	}
	if fixture.Revision.Revision.OriginKind != "root" {
		return domain.ErrInvalid
	}
	ref := fixture.Context.Current
	history := fixture.Revision.Revision.History
	digest, err := domain.Hash(history)
	if err != nil {
		return err
	}
	snapshot := domain.Snapshot{Version: domain.ContractVersion, CheckpointID: *ref.CheckpointID, Backend: fixture.Revision.Backend, ContextID: ref.ContextID, ExecutionThreadID: ref.ExecutionThreadID, CheckpointNS: ref.CheckpointNS, History: history, HistoryHash: digest, CreatedAt: fixture.Revision.Revision.CreatedAt}
	if _, err := r.Seal(ctx, snapshot); err != nil {
		return err
	}
	return r.update(ctx, func(c *domain.Catalog) (bool, error) {
		current, exists := c.Contexts[ref.ContextID]
		if exists && current.Current.Equal(ref) {
			existing := c.Revisions[ref.RevisionID]
			a, _ := domain.Hash(existing)
			b, _ := domain.Hash(fixture.Revision)
			if a != b || current.WorkspaceID != fixture.Context.WorkspaceID {
				return false, domain.ErrConflict
			}
			return false, nil
		}
		if exists {
			if expected == nil || !current.Current.Equal(*expected) || current.WorkspaceID != fixture.Context.WorkspaceID {
				return false, domain.ErrConflict
			}
		} else if expected != nil {
			return false, domain.ErrConflict
		}
		if _, exists := c.Revisions[ref.RevisionID]; exists {
			return false, domain.ErrConflict
		}
		for _, e := range c.Revisions {
			if e.Revision.Ref.ContextID == ref.ContextID && e.Revision.Ref.Generation >= ref.Generation {
				return false, domain.ErrConflict
			}
		}
		c.Contexts[ref.ContextID] = fixture.Context
		c.Revisions[ref.RevisionID] = fixture.Revision
		return true, nil
	})
}
func (r *Repository) Revision(ctx context.Context, ref domain.Ref) (domain.Envelope, error) {
	if err := ref.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	c, err := r.State(ctx)
	if err != nil {
		return domain.Envelope{}, err
	}
	env, exists := c.Revisions[ref.RevisionID]
	if !exists {
		return domain.Envelope{}, domain.ErrNotFound
	}
	if !env.Revision.Ref.Equal(ref) {
		return domain.Envelope{}, domain.ErrRefMismatch
	}
	snapshot, err := r.Snapshot(ctx, *ref.CheckpointID)
	if err != nil {
		return domain.Envelope{}, err
	}
	if err := snapshot.Matches(env); err != nil {
		return domain.Envelope{}, err
	}
	return env, nil
}
func (r *Repository) Current(ctx context.Context, workspace, id string) (domain.Envelope, error) {
	c, err := r.State(ctx)
	if err != nil {
		return domain.Envelope{}, err
	}
	context, ok := c.Contexts[id]
	if !ok {
		return domain.Envelope{}, domain.ErrNotFound
	}
	if context.WorkspaceID != workspace {
		return domain.Envelope{}, domain.ErrRefMismatch
	}
	return r.Revision(ctx, context.Current)
}
func (r *Repository) Run(ctx context.Context, id string) (domain.Run, error) {
	c, err := r.State(ctx)
	if err != nil {
		return domain.Run{}, err
	}
	run, ok := c.Runs[id]
	if !ok {
		return domain.Run{}, domain.ErrNotFound
	}
	return run, nil
}
func (r *Repository) Binding(ctx context.Context, agentID string) (domain.Run, domain.Envelope, error) {
	c, err := r.State(ctx)
	if err != nil {
		return domain.Run{}, domain.Envelope{}, err
	}
	for _, run := range c.Runs {
		if run.AgentRunID == agentID {
			env, err := r.Revision(ctx, run.Base)
			return run, env, err
		}
	}
	return domain.Run{}, domain.Envelope{}, domain.ErrNotFound
}
func (r *Repository) Admit(ctx context.Context, request domain.Request, profile string) (domain.Run, error) {
	if err := request.Validate(); err != nil {
		return domain.Run{}, err
	}
	if !domain.ID(profile) {
		return domain.Run{}, domain.ErrInvalid
	}
	hash, err := domain.RequestHash(request, profile)
	if err != nil {
		return domain.Run{}, err
	}
	// Read and validate the exact immutable source, never a fallback current.
	base, err := r.Revision(ctx, request.Expected)
	if err != nil {
		return domain.Run{}, err
	}
	messages, err := base.Revision.History.Messages()
	if err != nil {
		return domain.Run{}, err
	}
	identity, err := domain.Hash(struct {
		Workspace string `json:"workspace"`
		Key       string `json:"key"`
	}{request.WorkspaceID, request.Key})
	if err != nil {
		return domain.Run{}, err
	}
	runID := domain.StableID("run", identity, 0)
	inputID := domain.StableID("message", runID, 0)
	var admitted domain.Run
	err = r.update(ctx, func(c *domain.Catalog) (bool, error) {
		for _, run := range c.Runs {
			if run.WorkspaceID == request.WorkspaceID && run.Key == request.Key {
				if run.RequestHash != hash {
					return false, domain.ErrIdempotency
				}
				admitted = run
				return false, nil
			}
		}
		current, ok := c.Contexts[request.ContextID]
		if !ok {
			return false, domain.ErrNotFound
		}
		if current.WorkspaceID != request.WorkspaceID || !current.Current.Equal(request.Expected) {
			return false, domain.ErrConflict
		}
		for _, run := range c.Runs {
			if run.TaskID == request.ContextID && run.Settlement == nil {
				return false, domain.ErrBusy
			}
		}
		ids := make([]string, 0, len(messages)+1)
		for _, message := range messages {
			if message.ID == inputID {
				return false, domain.ErrInvalid
			}
			ids = append(ids, message.ID)
		}
		ids = append(ids, inputID)
		admitted = domain.Run{ID: runID, TaskID: request.ContextID, WorkspaceID: request.WorkspaceID, Kind: "main", Status: "pending", Base: request.Expected, Input: []domain.Message{{ID: inputID, Role: "user", Content: request.Input}}, Origin: "direct_user", OriginMessageID: inputID, Key: request.Key, RequestHash: hash, ExecutionThreadID: request.Expected.ExecutionThreadID, CheckpointNS: request.Expected.CheckpointNS, AgentRunID: domain.StableID("agent", runID, 0), Phase: "admitted", ModelProfile: profile, InputMessageIDs: ids}
		c.Runs[runID] = admitted
		return true, nil
	})
	return admitted, err
}
func (r *Repository) Progress(ctx context.Context, id, phase, status string, revision uint64) (domain.Run, error) {
	var result domain.Run
	err := r.update(ctx, func(c *domain.Catalog) (bool, error) {
		run, ok := c.Runs[id]
		if !ok {
			return false, domain.ErrNotFound
		}
		result = run
		if run.Settlement != nil || revision < run.AgentRevision {
			return false, nil
		}
		if run.Phase == phase && run.Status == status && run.AgentRevision == revision {
			return false, nil
		}
		run.Phase = phase
		run.Status = status
		run.AgentRevision = revision
		run.Reason = ""
		switch phase {
		case "awaiting-approval":
			run.Reason = "tool approval required"
		case "outcome-unknown":
			run.Reason = "tool result unknown; explicit verification required"
		case "canceled":
			run.Reason = "agent execution canceled"
		case "failed":
			run.Reason = "agent execution failed"
		}
		c.Runs[id] = run
		result = run
		return true, nil
	})
	return result, err
}
func (r *Repository) Settle(ctx context.Context, id string, snapshotID *string, status string) (domain.Run, error) {
	var snapshot domain.Snapshot
	if snapshotID != nil {
		var err error
		snapshot, err = r.Snapshot(ctx, *snapshotID)
		if err != nil {
			return domain.Run{}, err
		}
		if snapshot.RunID != id {
			return domain.Run{}, domain.ErrExecutionIdentity
		}
	}
	if status != "success" && status != "error" && status != "timeout" && status != "interrupted" {
		return domain.Run{}, domain.ErrInvalid
	}
	if (status == "success") != (snapshotID != nil) {
		return domain.Run{}, domain.ErrInvalid
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	var result domain.Run
	err := r.update(ctx, func(c *domain.Catalog) (bool, error) {
		run, ok := c.Runs[id]
		if !ok {
			return false, domain.ErrNotFound
		}
		result = run
		if run.Settlement != nil {
			return false, nil
		}
		if status == "success" && (snapshot.Base == nil || !snapshot.Base.Equal(run.Base) || snapshot.Backend != c.Revisions[run.Base.RevisionID].Backend) {
			return false, domain.ErrExecutionIdentity
		}
		settlement := domain.Settlement{RunID: id, TaskID: run.TaskID, Kind: "main", Status: status, FinalCheckpointID: snapshotID, Publication: "not_applicable", EventID: domain.StableID("event", id, 0)}
		if status == "success" {
			run.ResultRef = snapshotID
			current := c.Contexts[run.TaskID]
			if !current.Current.Equal(run.Base) {
				settlement.Publication = "superseded"
			} else {
				base := c.Revisions[run.Base.RevisionID]
				generation := uint64(1)
				for _, env := range c.Revisions {
					if env.Revision.Ref.ContextID == run.TaskID && env.Revision.Ref.Generation >= generation {
						generation = env.Revision.Ref.Generation + 1
					}
				}
				ref := domain.Ref{ContextID: run.TaskID, RevisionID: domain.StableID("revision", id, 0), Generation: generation, ExecutionThreadID: run.ExecutionThreadID, CheckpointNS: run.CheckpointNS, CheckpointID: snapshotID, PayloadMode: "checkpoint"}
				env := domain.Envelope{Version: domain.ContractVersion, RecordType: "context_revision", HashProfile: domain.HashProfile, Backend: base.Backend, Revision: domain.Revision{Ref: ref, Sources: []domain.Source{{Source: run.Base, Position: 0}}, History: snapshot.History, ProjectionStatus: "valid", OriginKind: "run_settled", OriginID: domain.Pointer(id), CreatedAt: snapshot.CreatedAt, InitialMessageIDs: []string{}, RepairManifest: []json.RawMessage{}, Issues: []json.RawMessage{}}}
				messages, _ := snapshot.History.Messages()
				for _, message := range messages {
					env.Revision.InitialMessageIDs = append(env.Revision.InitialMessageIDs, message.ID)
				}
				hash, err := env.ExpectedHash()
				if err != nil {
					return false, err
				}
				env.Revision.ContentHash = hash
				if _, exists := c.Revisions[ref.RevisionID]; exists {
					return false, domain.ErrConflict
				}
				c.Revisions[ref.RevisionID] = env
				current.Current = ref
				c.Contexts[run.TaskID] = current
				settlement.Publication = "published"
				settlement.ContextRevision = &ref
			}
		}
		run.Status = status
		if status == "timeout" {
			run.Reason = "execution deadline exceeded"
		}
		run.Settlement = &settlement
		run.SettledAt = &created
		c.Runs[id] = run
		c.Events[settlement.EventID] = domain.Event{ID: settlement.EventID, Kind: "MainRunSettled", RunID: id, Settlement: settlement}
		result = run
		return true, nil
	})
	return result, err
}
