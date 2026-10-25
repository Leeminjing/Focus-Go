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
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest-agent/trees/filecheckpoint"
	"github.com/Leeminjing/go-forest-agent/trees/manualapproval"
	"github.com/Leeminjing/go-forest-agent/trees/openairesponses"
	"github.com/Leeminjing/go-forest-agent/trees/responsejournal"
	"github.com/Leeminjing/go-forest/forest"
	"github.com/Leeminjing/go-forest/forest/compose"
)

type Options struct {
	Coding            *domain.Equipment
	Responses         *openairesponses.Config
	Tools             []ToolMount
	EventHandler      func(context.Context, agent.Event) error
	WorkspaceObserver func(workspace.Stage, domain.Receipt) error
	Directory         string
	Model             agent.Model
	Tool              agent.Tool
	RequireApproval   bool
	ModelProfile      string
	Observer          func(Stage, domain.Run) error
}
type Application struct {
	equipment *domain.Equipment
	forest    *forest.Forest
	observer  func(Stage, domain.Run) error
	profile   string
}
type ToolMount struct {
	ID         forest.TreeID
	Definition forest.Definition
	Capability forest.CapabilityID
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
	if len(options.Tools) > 0 && options.Tool != nil {
		return nil, domain.ErrInvalid
	}
	if options.Coding != nil {
		copy, err := domain.Copy(*options.Coding)
		if err != nil || copy.Validate() != nil {
			return nil, domain.ErrInvalid
		}
		options.Coding = &copy
		expected, err := CodingEquipment(copy.Workspace, copy.ModelProfile)
		if err != nil {
			return nil, err
		}
		a, _ := domain.Hash(expected.Tools)
		b, _ := domain.Hash(copy.Tools)
		if a != b {
			return nil, domain.ErrExecutionIdentity
		}
		if options.ModelProfile == "" {
			options.ModelProfile = copy.ModelProfile
		}
		if options.ModelProfile != copy.ModelProfile || options.Tool != nil || len(options.Tools) > 0 {
			return nil, domain.ErrInvalid
		}
	}
	if options.Responses != nil {
		profile, err := openairesponses.Profile(*options.Responses)
		if err != nil {
			return nil, err
		}
		if options.ModelProfile != "" && options.ModelProfile != profile {
			return nil, domain.ErrExecutionIdentity
		}
		options.ModelProfile = profile
		if options.Model != nil {
			return nil, domain.ErrInvalid
		}
	}
	if options.ModelProfile == "" {
		options.ModelProfile = "fixture-base-add-v1"
	}
	if !domain.ID(options.ModelProfile) {
		return nil, domain.ErrInvalid
	}
	if options.Model == nil {
		if options.Coding != nil && options.Responses == nil {
			return nil, domain.ErrInvalid
		}
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
	if options.Responses != nil {
		model = openairesponses.Definition(*options.Responses)
	}
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
		{"events", eventDefinition(options.EventHandler)}, {"approval", manualapproval.Definition()},
	}
	var operations []forest.Operation
	for _, item := range definitions {
		if item.id == "tool" && (options.Coding != nil || len(options.Tools) > 0) {
			continue
		}
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
		if binding.consumer == "agent" && binding.port == "tools" && (options.Coding != nil || len(options.Tools) > 0) {
			continue
		}
		operations = append(operations, forest.Connect(forest.Link{ID: domain.StableID("link", "application", i), Consumer: binding.consumer, Port: binding.port, Provider: binding.provider, Capability: binding.cap}))
	}
	if options.Responses != nil {
		definition := responsejournal.Definition(filepath.Join(options.Directory, "responses"))
		if err := f.Register(definition); err != nil {
			return nil, err
		}
		operations = append(operations, forest.AddRoot("response-journal", definition.Ref()), forest.Connect(forest.Link{ID: "model-journal", Consumer: "model", Port: "journal", Provider: "response-journal", Capability: "journal"}))
	}
	if len(options.Tools) > 0 {
		for index, mount := range options.Tools {
			if err := f.Register(mount.Definition); err != nil {
				return nil, err
			}
			operations = append(operations, forest.AddRoot(mount.ID, mount.Definition.Ref()), forest.Connect(forest.Link{ID: domain.StableID("tool-link", "application", index), Consumer: "agent", Port: "tools", Provider: mount.ID, Capability: mount.Capability}))
		}
	}
	if options.Coding != nil {
		root := workspace.RootDefinition(options.Coding.Workspace, filepath.Join(options.Directory, "workspace-receipts"), options.WorkspaceObserver)
		if err := f.Register(root); err != nil {
			return nil, err
		}
		spec := compose.Spec{Root: root.Ref()}
		for index, name := range []string{"workspace_search", "workspace_read", "workspace_edit"} {
			definition := workspace.ToolDefinition(name, options.Coding.Workspace.Preapproved)
			if err := f.Register(definition); err != nil {
				return nil, err
			}
			local := forest.TreeID(name)
			spec.Trees = append(spec.Trees, compose.Tree{ID: local, Definition: definition.Ref()})
			spec.Bindings = append(spec.Bindings, forest.Link{ID: name + "-files", Consumer: local, Port: "files", Capability: "files"})
			provider := forest.TreeID("workspace/" + name)
			operations = append(operations, forest.Connect(forest.Link{ID: name + "-binding", Consumer: provider, Port: "binding", Provider: "repository", Capability: "repository"}), forest.Connect(forest.Link{ID: name + "-execution", Consumer: provider, Port: "execution", Provider: "agent", Capability: "executor"}), forest.Connect(forest.Link{ID: domain.StableID("coding-tool-link", "application", index), Consumer: "agent", Port: "tools", Provider: provider, Capability: "tool"}))
		}
		blueprint, err := compose.New(spec)
		if err != nil {
			return nil, err
		}
		instance, err := blueprint.Instantiate("workspace", "")
		if err != nil {
			return nil, err
		}
		operations = append(operations, instance.Plan(0).Operations...)
		definition := evidenceDefinition(options.Responses != nil)
		if err := f.Register(definition); err != nil {
			return nil, err
		}
		operations = append(operations, forest.AddRoot("evidence", definition.Ref()), forest.Connect(forest.Link{ID: "evidence-files", Consumer: "evidence", Port: "workspace", Provider: "workspace", Capability: "evidence"}), forest.Connect(forest.Link{ID: "workflow-evidence", Consumer: "focus", Port: "evidence", Provider: "evidence", Capability: "evidence"}))
		if options.Responses != nil {
			operations = append(operations, forest.Connect(forest.Link{ID: "evidence-model", Consumer: "evidence", Port: "journal", Provider: "response-journal", Capability: "journal"}))
		}
	}
	if _, err := f.Apply(context.Background(), forest.ChangePlan{Operations: operations}); err != nil {
		return nil, err
	}
	application := &Application{forest: f, observer: options.Observer, profile: options.ModelProfile, equipment: options.Coding}
	if options.Coding != nil {
		if err := application.repo(context.Background(), func(ctx context.Context, r store.Capability) error {
			return r.RegisterWorkspace(ctx, options.Coding.Workspace)
		}); err != nil {
			return nil, err
		}
	}
	ok = true
	return application, nil
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
	if a.equipment != nil {
		if request.Equipment != nil {
			x, _ := domain.Hash(*request.Equipment)
			y, _ := domain.Hash(*a.equipment)
			if x != y {
				return Outcome{}, domain.ErrExecutionIdentity
			}
		}
		copy, err := domain.Copy(*a.equipment)
		if err != nil {
			return Outcome{}, err
		}
		request.Equipment = &copy
	} else if request.Equipment != nil {
		return Outcome{}, domain.ErrExecutionIdentity
	}
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
	return a.Run(ctx, domain.Request{Equipment: run.Equipment, WorkspaceID: run.WorkspaceID, ContextID: run.TaskID, Expected: run.Base, Input: run.Input[0].Content, Key: run.Key})
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
