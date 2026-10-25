// Package app owns Focus product orchestration. It does not extend either
// Forest Core or the generic Agent Runtime with Focus-specific state.
package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/agentbridge"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/store"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest-agent/trees/events"
	"github.com/Leeminjing/go-forest-agent/trees/filecheckpoint"
	"github.com/Leeminjing/go-forest-agent/trees/manualapproval"
	"github.com/Leeminjing/go-forest/forest"
)

type Options struct {
	Directory       string
	Model           agent.Model
	Tool            agent.Tool
	RequireApproval bool
	ModelProfile    string
	Observer        func(Stage, domain.Run) error
}
type Application struct {
	forest   *forest.Forest
	observer func(Stage, domain.Run) error
	profile  string
}
type Outcome struct {
	Run      domain.Run `json:"run"`
	Current  domain.Ref `json:"current_ref"`
	Answer   string     `json:"answer"`
	Verified bool       `json:"verified"`
}

func New(options Options) (*Application, error) {
	if options.Directory == "" {
		return nil, domain.ErrInvalid
	}
	if options.ModelProfile == "" {
		options.ModelProfile = "fixture-base-add-v1"
	}
	if !domain.ID(options.ModelProfile) {
		return nil, domain.ErrInvalid
	}
	if options.Model == nil {
		options.Model = fixtureModel{}
	}
	if options.Tool == nil {
		options.Tool = addition{approval: options.RequireApproval}
	}
	f := forest.New(forest.Options{})
	ok := false
	defer func() {
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = f.Close(ctx)
		}
	}()
	model := forest.Definition{TypeID: "focus-go.model", Version: "1", Capabilities: []forest.CapabilityDescriptor{forest.Capability[agentbridge.Model]("model", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		var life forest.Lifecycle = forest.Hooks{}
		if supplied, ok := options.Model.(forest.Lifecycle); ok {
			life = supplied
		}
		return life, forest.Provide(env, "model", options.Model)
	}}
	tool := forest.Definition{TypeID: "focus-go.tool", Version: "1", Capabilities: []forest.CapabilityDescriptor{forest.Capability[agent.Tool]("tool", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		var life forest.Lifecycle = forest.Hooks{}
		if supplied, ok := options.Tool.(forest.Lifecycle); ok {
			life = supplied
		}
		return life, forest.Provide(env, "tool", options.Tool)
	}}
	definitions := []struct {
		id  forest.TreeID
		def forest.Definition
	}{
		{"focus", workflowDefinition(options.ModelProfile, options.Observer)},
		{"repository", store.Definition(filepath.Join(options.Directory, "focus"))},
		{"agent", agent.Definition(agent.Options{})}, {"history-model", agentbridge.Definition()},
		{"model", model}, {"tool", tool}, {"checkpoint", filecheckpoint.Definition(filepath.Join(options.Directory, "agent"))},
		{"events", events.Definition(events.New(32768))}, {"approval", manualapproval.Definition()},
	}
	var operations []forest.Operation
	for _, item := range definitions {
		if err := f.Register(item.def); err != nil {
			return nil, err
		}
		operations = append(operations, forest.AddRoot(item.id, item.def.Ref()))
	}
	for i, binding := range []struct {
		consumer forest.TreeID
		port     forest.PortID
		provider forest.TreeID
		cap      forest.CapabilityID
	}{
		{"focus", "repository", "repository", "repository"}, {"focus", "agent", "agent", "executor"},
		{"history-model", "source", "repository", "repository"}, {"history-model", "model", "model", "model"},
		{"agent", "model", "history-model", "model"}, {"agent", "tools", "tool", "tool"},
		{"agent", "checkpoint", "checkpoint", "checkpoint"}, {"agent", "events", "events", "events"}, {"agent", "approval", "approval", "approval"},
	} {
		operations = append(operations, forest.Connect(forest.Link{ID: domain.StableID("link", "application", i), Consumer: binding.consumer, Port: binding.port, Provider: binding.provider, Capability: binding.cap}))
	}
	if _, err := f.Apply(context.Background(), forest.ChangePlan{Operations: operations}); err != nil {
		return nil, err
	}
	ok = true
	return &Application{forest: f, observer: options.Observer, profile: options.ModelProfile}, nil
}
func (a *Application) Close(ctx context.Context) error { return a.forest.Close(ctx) }
func (a *Application) repo(ctx context.Context, fn func(context.Context, store.Capability) error) error {
	return a.forest.Invoke(ctx, func(ctx context.Context) error {
		return forest.Use[store.Capability](ctx, a.forest, "repository", "repository", func(repo store.Capability) error { return fn(ctx, repo) })
	})
}
func (a *Application) workflow(ctx context.Context, fn func(context.Context, workflowAPI) error) error {
	return a.forest.Invoke(ctx, func(ctx context.Context) error {
		return forest.Use[workflowAPI](ctx, a.forest, "focus", "workflow", func(workflow workflowAPI) error { return fn(ctx, workflow) })
	})
}
func (a *Application) Import(ctx context.Context, data []byte, expected *domain.Ref) error {
	var fixture domain.Fixture
	if err := domain.Strict(data, &fixture); err != nil {
		return err
	}
	if err := fixture.Validate(); err != nil {
		return err
	}
	return a.repo(ctx, func(ctx context.Context, repo store.Capability) error { return repo.Install(ctx, fixture, expected) })
}
func (a *Application) Current(ctx context.Context, workspace, id string) (domain.Envelope, error) {
	var result domain.Envelope
	err := a.repo(ctx, func(ctx context.Context, repo store.Capability) error {
		var err error
		result, err = repo.Current(ctx, workspace, id)
		return err
	})
	return result, err
}
func (a *Application) Revision(ctx context.Context, ref domain.Ref) (domain.Envelope, error) {
	var result domain.Envelope
	err := a.repo(ctx, func(ctx context.Context, repo store.Capability) error {
		var err error
		result, err = repo.Revision(ctx, ref)
		return err
	})
	return result, err
}
func (a *Application) Record(ctx context.Context, id string) (domain.Run, error) {
	var result domain.Run
	err := a.repo(ctx, func(ctx context.Context, repo store.Capability) error {
		var err error
		result, err = repo.Run(ctx, id)
		return err
	})
	return result, err
}
func (a *Application) State(ctx context.Context) (domain.Catalog, error) {
	var result domain.Catalog
	err := a.repo(ctx, func(ctx context.Context, repo store.Capability) error {
		var err error
		result, err = repo.State(ctx)
		return err
	})
	return result, err
}
func (a *Application) Snapshot(ctx context.Context, id string) (domain.Snapshot, error) {
	var result domain.Snapshot
	err := a.repo(ctx, func(ctx context.Context, repo store.Capability) error {
		var err error
		result, err = repo.Snapshot(ctx, id)
		return err
	})
	return result, err
}

func (a *Application) Run(ctx context.Context, request domain.Request) (Outcome, error) {
	if _, err := a.forest.Generation(ctx); err == nil {
		return Outcome{}, agent.ErrNestedScope
	}
	var run domain.Run
	err := a.workflow(ctx, func(ctx context.Context, w workflowAPI) error {
		var err error
		run, err = w.Prepare(ctx, request)
		return err
	})
	if err != nil {
		return Outcome{Run: run}, err
	}
	if run.Settlement != nil {
		return a.outcome(ctx, run.ID)
	}
	_, driveErr := agent.Continue(ctx, a.forest, "agent", run.AgentRunID)
	if errors.Is(driveErr, agent.ErrBusy) || errors.Is(driveErr, agent.ErrConflict) {
		result, _ := a.outcome(ctx, run.ID)
		return result, errors.Join(domain.ErrBusy, driveErr)
	}
	// Metadata finalization is bounded and keeps no execution Scope alive.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var record agent.Run
	err = a.workflow(finishCtx, func(ctx context.Context, w workflowAPI) error {
		var err error
		run, record, err = w.Inspect(ctx, run.ID)
		return err
	})
	if err != nil {
		return Outcome{Run: run}, errors.Join(driveErr, err)
	}
	if record.Status == agent.Completed {
		if err := observe(a.observer, AgentCompleted, run); err != nil {
			return Outcome{Run: run}, err
		}
		if err := a.workflow(finishCtx, func(ctx context.Context, w workflowAPI) error { _, err := w.Seal(ctx, run.ID); return err }); err != nil {
			return Outcome{Run: run}, err
		}
		if err := observe(a.observer, ResultSealed, run); err != nil {
			return Outcome{Run: run}, err
		}
	}
	err = a.workflow(finishCtx, func(ctx context.Context, w workflowAPI) error {
		var err error
		run, err = w.Finish(ctx, run.ID, errors.Is(driveErr, context.DeadlineExceeded))
		return err
	})
	if err != nil {
		return Outcome{Run: run}, errors.Join(driveErr, err)
	}
	if run.Settlement != nil {
		if err := observe(a.observer, SettlementCommitted, run); err != nil {
			return Outcome{Run: run}, err
		}
	}
	result, err := a.outcome(finishCtx, run.ID)
	return result, errors.Join(driveErr, err)
}
func (a *Application) Resume(ctx context.Context, id string) (Outcome, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	if run.ModelProfile != a.profile {
		return Outcome{}, domain.ErrExecutionIdentity
	}
	return a.Run(ctx, domain.Request{WorkspaceID: run.WorkspaceID, ContextID: run.TaskID, Expected: run.Base, Input: run.Input[0].Content, Key: run.Key})
}
func (a *Application) outcome(ctx context.Context, id string) (Outcome, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	current, err := a.Current(ctx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return Outcome{Run: run}, err
	}
	result := Outcome{Run: run, Current: current.Revision.Ref}
	if run.ResultRef != nil {
		snapshot, err := a.Snapshot(ctx, *run.ResultRef)
		if err != nil {
			return result, err
		}
		messages, err := snapshot.History.Messages()
		if err != nil {
			return result, err
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if last.Role == "assistant" {
				result.Answer = last.Content
			}
		}
		if run.Settlement != nil && run.Settlement.ContextRevision != nil {
			env, err := a.Revision(ctx, *run.Settlement.ContextRevision)
			if err != nil {
				return result, err
			}
			if err := snapshot.Matches(env); err != nil {
				return result, err
			}
			result.Verified = true
		}
	}
	return result, nil
}

// Management calls require a trusted host. Recover is only safe after the
// host confirms that the old executor is stopped; it is never automatic.
func (a *Application) manage(ctx context.Context, id string, fn func(context.Context, agent.Executor, string) (agent.Run, error)) (agent.Run, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return agent.Run{}, err
	}
	if run.ModelProfile != a.profile {
		return agent.Run{}, domain.ErrExecutionIdentity
	}
	var result agent.Run
	err = a.forest.Invoke(ctx, func(ctx context.Context) error {
		return forest.Use[agent.Executor](ctx, a.forest, "agent", "executor", func(executor agent.Executor) error {
			var err error
			result, err = fn(ctx, executor, run.AgentRunID)
			return err
		})
	})
	return result, err
}
func (a *Application) AgentState(ctx context.Context, id string) (agent.Run, error) {
	return a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		return e.Read(ctx, agentID)
	})
}
func (a *Application) Decide(ctx context.Context, id string, revision uint64, call string, decision agent.Decision) (agent.Run, error) {
	return a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		return e.Decide(ctx, agentID, revision, call, decision)
	})
}
func (a *Application) Recover(ctx context.Context, id string) (agent.Run, error) {
	return a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		return e.Recover(ctx, agentID)
	})
}
func (a *Application) Resolve(ctx context.Context, id, call, result string) (agent.Run, error) {
	return a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		return e.Resolve(ctx, agentID, call, result)
	})
}
