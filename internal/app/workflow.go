package app

import (
	"context"
	"errors"

	"github.com/Leeminjing/Focus-Go/internal/agentbridge"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest/forest"
)

type repository interface {
	Run(context.Context, string) (domain.Run, error)
	Binding(context.Context, string) (domain.Run, domain.Envelope, error)
	Admit(context.Context, domain.Request, string) (domain.Run, error)
	Progress(context.Context, string, string, string, uint64) (domain.Run, error)
	Seal(context.Context, domain.Snapshot) (domain.Snapshot, error)
	Snapshot(context.Context, string) (domain.Snapshot, error)
	Settle(context.Context, string, *string, string) (domain.Run, error)
}
type workflowAPI interface {
	Prepare(context.Context, domain.Request) (domain.Run, error)
	Inspect(context.Context, string) (domain.Run, agent.Run, error)
	Seal(context.Context, string) (domain.Snapshot, error)
	Finish(context.Context, string, bool) (domain.Run, error)
}
type workflow struct {
	repo     forest.Ref[repository]
	agent    forest.Ref[agent.Executor]
	profile  string
	observer func(Stage, domain.Run) error
}
type Stage string

const (
	Admitted            Stage = "admitted"
	AgentCompleted      Stage = "agent-completed"
	ResultSealed        Stage = "result-sealed"
	SettlementCommitted Stage = "settlement-committed"
)

func observe(fn func(Stage, domain.Run) error, stage Stage, run domain.Run) error {
	if fn == nil {
		return nil
	}
	copy, err := domain.Copy(run)
	if err != nil {
		return err
	}
	return fn(stage, copy)
}
func (w *workflow) Prepare(ctx context.Context, request domain.Request) (domain.Run, error) {
	var run domain.Run
	err := w.repo.Use(ctx, func(repo repository) error { var err error; run, err = repo.Admit(ctx, request, w.profile); return err })
	if err != nil {
		return run, err
	}
	if run.Settlement != nil {
		return run, nil
	}
	if err := observe(w.observer, Admitted, run); err != nil {
		return run, err
	}
	var record agent.Run
	err = w.agent.Use(ctx, func(executor agent.Executor) error {
		var err error
		record, err = executor.Read(ctx, run.AgentRunID)
		if errors.Is(err, agent.ErrNotFound) {
			record, err = executor.Create(ctx, run.AgentRunID, run.Input[0].Content)
			if errors.Is(err, agent.ErrConflict) {
				record, err = executor.Read(ctx, run.AgentRunID)
			}
		}
		if err != nil {
			return err
		}
		return agentbridge.CheckInput(run, record.ID, record.Messages)
	})
	if err != nil {
		return run, err
	}
	return w.progress(ctx, run, record)
}
func (w *workflow) Inspect(ctx context.Context, id string) (domain.Run, agent.Run, error) {
	var run domain.Run
	err := w.repo.Use(ctx, func(repo repository) error { var err error; run, err = repo.Run(ctx, id); return err })
	if err != nil {
		return run, agent.Run{}, err
	}
	if run.ModelProfile != w.profile {
		return run, agent.Run{}, domain.ErrExecutionIdentity
	}
	var record agent.Run
	err = w.agent.Use(ctx, func(executor agent.Executor) error {
		var err error
		record, err = executor.Read(ctx, run.AgentRunID)
		if err != nil {
			return err
		}
		return agentbridge.CheckInput(run, record.ID, record.Messages)
	})
	return run, record, err
}
func focusStatus(status agent.Status) string {
	switch status {
	case agent.Completed:
		return "success"
	case agent.Failed:
		return "error"
	case agent.Canceled, agent.AwaitingApproval, agent.OutcomeUnknown:
		return "interrupted"
	default:
		return "running"
	}
}
func (w *workflow) progress(ctx context.Context, run domain.Run, record agent.Run) (domain.Run, error) {
	err := w.repo.Use(ctx, func(repo repository) error {
		var err error
		run, err = repo.Progress(ctx, run.ID, string(record.Status), focusStatus(record.Status), record.Revision)
		return err
	})
	return run, err
}
func (w *workflow) Seal(ctx context.Context, id string) (domain.Snapshot, error) {
	run, record, err := w.Inspect(ctx, id)
	if err != nil {
		return domain.Snapshot{}, err
	}
	var base domain.Envelope
	err = w.repo.Use(ctx, func(repo repository) error { _, env, err := repo.Binding(ctx, run.AgentRunID); base = env; return err })
	if err != nil {
		return domain.Snapshot{}, err
	}
	snapshot, err := agentbridge.Compose(run, base, record)
	if err != nil {
		return domain.Snapshot{}, err
	}
	err = w.repo.Use(ctx, func(repo repository) error { var err error; snapshot, err = repo.Seal(ctx, snapshot); return err })
	return snapshot, err
}
func (w *workflow) Finish(ctx context.Context, id string, timedout bool) (domain.Run, error) {
	var existing domain.Run
	err := w.repo.Use(ctx, func(repo repository) error { var err error; existing, err = repo.Run(ctx, id); return err })
	if err != nil {
		return existing, err
	}
	if existing.Settlement != nil {
		return existing, nil
	}
	run, record, err := w.Inspect(ctx, id)
	if err != nil {
		return run, err
	}
	run, err = w.progress(ctx, run, record)
	if err != nil {
		return run, err
	}
	switch record.Status {
	case agent.Completed:
		checkpoint := domain.StableID("checkpoint", run.ID, 0)
		var snapshot domain.Snapshot
		err = w.repo.Use(ctx, func(repo repository) error { var err error; snapshot, err = repo.Snapshot(ctx, checkpoint); return err })
		if err != nil {
			return run, err
		}
		if snapshot.AgentRef == nil || snapshot.AgentRef.RunID != record.ID {
			return run, domain.ErrExecutionIdentity
		}
		var base domain.Envelope
		if err := w.repo.Use(ctx, func(repo repository) error {
			var err error
			_, base, err = repo.Binding(ctx, run.AgentRunID)
			return err
		}); err != nil {
			return run, err
		}
		if err := agentbridge.Verify(run, base, snapshot); err != nil {
			return run, err
		}
		err = w.repo.Use(ctx, func(repo repository) error {
			var err error
			run, err = repo.Settle(ctx, run.ID, &checkpoint, "success")
			return err
		})
	case agent.Canceled, agent.Failed:
		err = w.repo.Use(ctx, func(repo repository) error {
			var err error
			status := focusStatus(record.Status)
			if record.Status == agent.Canceled && timedout {
				status = "timeout"
			}
			run, err = repo.Settle(ctx, run.ID, nil, status)
			return err
		})
	}
	return run, err
}
func workflowDefinition(profile string, observer func(Stage, domain.Run) error) forest.Definition {
	return forest.Definition{TypeID: "focus-go.workflow", Version: "1", Ports: []forest.PortDescriptor{forest.Port[repository]("repository", forest.Required(), forest.During(forest.Construction)), forest.Port[agent.Executor]("agent", forest.Required())}, Capabilities: []forest.CapabilityDescriptor{forest.Capability[workflowAPI]("workflow", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		repo, err := forest.Reference[repository](env, "repository")
		if err != nil {
			return nil, err
		}
		runner, err := forest.Reference[agent.Executor](env, "agent")
		if err != nil {
			return nil, err
		}
		return forest.Hooks{}, forest.Provide(env, "workflow", &workflow{repo: repo, agent: runner, profile: profile, observer: observer})
	}}
}
