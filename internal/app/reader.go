package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/store"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest-agent/trees/openairesponses"
	"github.com/Leeminjing/go-forest/forest"
)

func OpenReader(directory string) (*Application, error) {
	if directory == "" {
		return nil, domain.ErrInvalid
	}
	f := forest.New(forest.Options{})
	definition := store.Definition(filepath.Join(directory, "focus"))
	if err := f.Register(definition); err != nil {
		return nil, err
	}
	if _, err := f.Apply(context.Background(), forest.ChangePlan{Operations: []forest.Operation{forest.AddRoot("repository", definition.Ref())}}); err != nil {
		f.Close(context.Background())
		return nil, err
	}
	return &Application{forest: f, directory: directory, managementOnly: true}, nil
}

type managementModel struct{}

func (managementModel) Generate(context.Context, agent.ModelRequest, func(string) error) (agent.ModelResult, error) {
	return agent.ModelResult{}, domain.ErrExecutionIdentity
}
func OpenManager(directory string, equipment domain.Equipment) (*Application, error) {
	return New(Options{Directory: directory, Coding: &equipment, ModelProfile: equipment.ModelProfile, Model: managementModel{}, managementOnly: true})
}
func (a *Application) RegisterWorkspace(ctx context.Context, w domain.Workspace) error {
	return a.repo(ctx, func(ctx context.Context, r store.Capability) error { return r.RegisterWorkspace(ctx, w) })
}
func (a *Application) Result(ctx context.Context, id string) (Outcome, error) {
	return a.outcome(ctx, id)
}

// FinishSaved verifies the self-contained result without constructing model,
// workspace or Agent execution trees. False means no sealed result exists.
func (a *Application) FinishSaved(ctx context.Context, id string) (Outcome, bool, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return Outcome{}, false, err
	}
	if run.Settlement != nil {
		value, err := a.outcome(ctx, id)
		return value, true, err
	}
	found := false
	err = a.repo(ctx, func(ctx context.Context, r store.Capability) error {
		var err error
		run, found, err = settleSaved(ctx, run, r)
		return err
	})
	if err != nil || !found {
		return Outcome{Run: run}, found, err
	}
	value, err := a.outcome(ctx, id)
	return value, true, err
}
func (a *Application) CreateContext(ctx context.Context, workspaceID, id, text string) (domain.Ref, error) {
	if !domain.ID(workspaceID) || !domain.ID(id) || text == "" || !domain.Text(text) {
		return domain.Ref{}, domain.ErrInvalid
	}
	state, err := a.State(ctx)
	if err != nil {
		return domain.Ref{}, err
	}
	if _, exists := state.Workspaces[workspaceID]; !exists {
		return domain.Ref{}, domain.ErrNotFound
	}
	ref := domain.Ref{ContextID: id, RevisionID: domain.StableID("root-revision", id, 0), Generation: 1, ExecutionThreadID: domain.StableID("thread", id, 0), CheckpointNS: "focus-go", CheckpointID: domain.Pointer(domain.StableID("root-checkpoint", id, 0)), PayloadMode: "checkpoint"}
	item, err := domain.NewItem(domain.Message{ID: domain.StableID("root-message", id, 0), Role: "user", Content: text}, "direct_user", "revision", []json.RawMessage{})
	if err != nil {
		return ref, err
	}
	e := domain.Envelope{Version: domain.ContractVersion, RecordType: "context_revision", HashProfile: domain.HashProfile, Backend: domain.Backend{Engine: "focus-go-snapshot", Format: "context-snapshot-v1", StoreID: "focus-results"}, Revision: domain.Revision{Ref: ref, Sources: []domain.Source{}, History: domain.History{SchemaVersion: 2, Authored: []domain.Item{item}, Execution: []domain.Item{item}, BindingRefs: []json.RawMessage{}, SelectionVersion: domain.SelectionVersion}, ProjectionStatus: "valid", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), OriginKind: "root", InitialMessageIDs: []string{item.ItemID}, RepairManifest: []json.RawMessage{}, Issues: []json.RawMessage{}}}
	e.Revision.ContentHash, err = e.ExpectedHash()
	if err != nil {
		return ref, err
	}
	fixture := domain.Fixture{Version: domain.ContractVersion, Context: domain.Context{WorkspaceID: workspaceID, ID: id, Lifecycle: "active", Current: ref}, Revision: e}
	if existing, ok := state.Contexts[id]; ok {
		if existing.WorkspaceID != workspaceID {
			return ref, domain.ErrConflict
		}
		old := state.Revisions[existing.Current.RevisionID]
		if old.Revision.Ref.Equal(ref) && old.Revision.ContentHash == e.Revision.ContentHash {
			return ref, nil
		}
		return ref, domain.ErrConflict
	}
	raw, err := domain.Marshal(fixture)
	if err != nil {
		return ref, err
	}
	return ref, a.Import(ctx, raw, nil)
}
func ResponsesEquipment(binding domain.Workspace, config openairesponses.Config) (domain.Equipment, error) {
	if config.Endpoint == "" {
		config.Endpoint = "https://api.openai.com/v1/responses"
	}
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = 4096
	}
	if config.Timeout == 0 {
		config.Timeout = 90 * time.Second
	}
	profile, err := openairesponses.Profile(config)
	if err != nil {
		return domain.Equipment{}, err
	}
	e, err := CodingEquipment(binding, profile)
	if err != nil {
		return e, err
	}
	e.Model = &domain.ModelSettings{Endpoint: config.Endpoint, Model: config.Model, Instructions: config.Instructions, ReasoningEffort: config.ReasoningEffort, MaxOutputTokens: config.MaxOutputTokens, Timeout: int64(config.Timeout)}
	return e, e.Validate()
}
func ModelConfig(e domain.Equipment, key string) (openairesponses.Config, error) {
	if e.Model == nil {
		return openairesponses.Config{}, domain.ErrInvalid
	}
	m := e.Model
	config := openairesponses.Config{Endpoint: m.Endpoint, APIKey: key, Model: m.Model, Instructions: m.Instructions, ReasoningEffort: m.ReasoningEffort, MaxOutputTokens: m.MaxOutputTokens, Timeout: time.Duration(m.Timeout)}
	profile, err := openairesponses.Profile(config)
	if err != nil || profile != e.ModelProfile {
		return config, domain.ErrExecutionIdentity
	}
	return config, nil
}

func (a *Application) Reconcile(ctx context.Context, id string, expected uint64, callID, operator string) (agent.Run, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return agent.Run{}, err
	}
	return a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		record, err := e.Read(ctx, agentID)
		if err != nil {
			return record, err
		}
		if record.Revision != expected || record.Status != agent.OutcomeUnknown || record.Current >= len(record.Calls) || record.Calls[record.Current].Call.ID != callID {
			return record, agent.ErrConflict
		}
		var result string
		err = forest.Use[workspace.EvidenceAccess](ctx, a.forest, "workspace", "evidence", func(w workspace.EvidenceAccess) error {
			var err error
			result, err = w.Reconcile(ctx, run, record, operator)
			return err
		})
		if err != nil {
			return record, err
		}
		return e.Resolve(ctx, agentID, callID, result)
	})
}

func (a *Application) Preview(ctx context.Context, id string, expected uint64, callID string) (map[string]any, error) {
	run, err := a.Record(ctx, id)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	_, err = a.manage(ctx, id, func(ctx context.Context, e agent.Executor, agentID string) (agent.Run, error) {
		record, err := e.Read(ctx, agentID)
		if err != nil {
			return record, err
		}
		if record.Revision != expected || record.Current < 0 || record.Current >= len(record.Calls) || record.Calls[record.Current].Call.ID != callID {
			return record, agent.ErrConflict
		}
		err = forest.Use[workspace.EvidenceAccess](ctx, a.forest, "workspace", "evidence", func(w workspace.EvidenceAccess) error {
			var err error
			value, err = w.Preview(ctx, run, record)
			return err
		})
		return record, err
	})
	return value, err
}
