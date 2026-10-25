// Package agentbridge translates supported Focus history without changing the
// generic Agent Runtime or interpreting a Python execution checkpoint.
package agentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest/forest"
)

type BindingReader interface {
	Binding(context.Context, string) (domain.Run, domain.Envelope, error)
}
type Model interface {
	Generate(context.Context, agent.ModelRequest, func(string) error) (agent.ModelResult, error)
}
type historyModel struct {
	source forest.Ref[BindingReader]
	model  forest.Ref[Model]
}

func (h *historyModel) Generate(ctx context.Context, request agent.ModelRequest, emit func(string) error) (agent.ModelResult, error) {
	var run domain.Run
	var base domain.Envelope
	err := h.source.Use(ctx, func(reader BindingReader) error {
		var err error
		run, base, err = reader.Binding(ctx, request.RunID)
		return err
	})
	if err != nil {
		return agent.ModelResult{}, err
	}
	if err := CheckInput(run, request.RunID, request.Messages); err != nil {
		return agent.ModelResult{}, err
	}
	previous, err := base.Revision.History.Messages()
	if err != nil {
		return agent.ModelResult{}, err
	}
	messages := make([]agent.Message, 0, len(previous)+len(request.Messages))
	calls := map[string]bool{}
	for _, message := range previous {
		for _, call := range message.Calls {
			calls[call.ID] = true
		}
		messages = append(messages, ToAgent(message))
	}
	messages = append(messages, request.Messages...)
	request.Messages = messages
	if _, err := domain.Marshal(request.Tools); err != nil {
		return agent.ModelResult{}, errors.Join(domain.ErrUnsupportedHistory, err)
	}
	var result agent.ModelResult
	err = h.model.Use(ctx, func(model Model) error {
		var err error
		result, err = model.Generate(ctx, request, emit)
		if err != nil {
			return err
		}
		if _, err := domain.Marshal(result); err != nil {
			return err
		}
		for _, call := range result.Calls {
			if calls[call.ID] {
				return domain.ErrExecutionIdentity
			}
			var args map[string]json.RawMessage
			if !domain.ID(call.ID) || !domain.ID(call.Name) || domain.Strict(call.Arguments, &args) != nil || args == nil {
				return domain.ErrInvalid
			}
		}
		return nil
	})
	return result, err
}
func CheckInput(run domain.Run, id string, messages []agent.Message) error {
	if run.Validate() != nil || run.AgentRunID != id || len(messages) == 0 || messages[0].Role != "user" || messages[0].Content != run.Input[0].Content || messages[0].CallID != "" || len(messages[0].Calls) != 0 {
		return domain.ErrExecutionIdentity
	}
	for _, message := range messages[1:] {
		if message.Role != "assistant" && message.Role != "tool" {
			return domain.ErrExecutionIdentity
		}
	}
	return nil
}
func ToAgent(message domain.Message) agent.Message {
	role := message.Role
	if role == "human" {
		role = "user"
	}
	if role == "ai" {
		role = "assistant"
	}
	result := agent.Message{Role: role, Content: message.Content, CallID: message.CallID}
	for _, call := range message.Calls {
		result.Calls = append(result.Calls, agent.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Args})
	}
	return result
}
func Compose(run domain.Run, base domain.Envelope, record agent.Run) (domain.Snapshot, error) {
	if err := record.Validate(); err != nil {
		return domain.Snapshot{}, err
	}
	if record.Status != agent.Completed {
		return domain.Snapshot{}, domain.ErrInvalid
	}
	if len(record.Messages) == 0 || record.Messages[len(record.Messages)-1].Role != "assistant" || len(record.Messages[len(record.Messages)-1].Calls) != 0 {
		return domain.Snapshot{}, domain.ErrInvalid
	}
	if err := CheckInput(run, record.ID, record.Messages); err != nil {
		return domain.Snapshot{}, err
	}
	if !run.Base.Equal(base.Revision.Ref) {
		return domain.Snapshot{}, domain.ErrRefMismatch
	}
	history, err := domain.Copy(base.Revision.History)
	if err != nil {
		return domain.Snapshot{}, err
	}
	source, err := domain.Marshal(struct {
		RunID      string `json:"run_id"`
		RevisionID string `json:"source_revision_id"`
	}{run.ID, run.Base.RevisionID})
	if err != nil {
		return domain.Snapshot{}, err
	}
	for i, message := range record.Messages {
		id := domain.StableID("message", run.ID, i)
		focus := domain.Message{ID: id, Role: message.Role, Content: message.Content, CallID: message.CallID}
		for _, call := range message.Calls {
			focus.Calls = append(focus.Calls, domain.Call{ID: call.ID, Name: call.Name, Args: call.Arguments, Type: "tool_call"})
		}
		origin, scope := "provider", "execution"
		if message.Role == "user" {
			origin = "direct_user"
			scope = "revision"
		}
		if message.Role == "tool" {
			origin = "tool"
		}
		item, err := domain.NewItem(focus, origin, scope, []json.RawMessage{source})
		if err != nil {
			return domain.Snapshot{}, err
		}
		history.Execution = append(history.Execution, item)
		if message.Role == "user" {
			history.Authored = append(history.Authored, item)
		}
	}
	if err := history.Validate(); err != nil {
		return domain.Snapshot{}, err
	}
	hash, err := domain.Hash(history)
	if err != nil {
		return domain.Snapshot{}, err
	}
	raw, err := domain.Marshal(record)
	if err != nil {
		return domain.Snapshot{}, err
	}
	return domain.Snapshot{Version: domain.ContractVersion, CheckpointID: domain.StableID("checkpoint", run.ID, 0), Backend: base.Backend, ContextID: run.TaskID, ExecutionThreadID: run.ExecutionThreadID, CheckpointNS: run.CheckpointNS, RunID: run.ID, Base: &run.Base, AgentRef: &domain.AgentCheckpointRef{Engine: "go-forest-agent", Format: "run-v1", StoreID: "agent-checkpoints", RunID: record.ID, Revision: record.Revision}, AgentRecord: raw, History: history, HistoryHash: hash, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

// Verify checks the sealed runtime format at the adapter seam, keeping Agent
// record interpretation out of the language-neutral domain and repository.
func Verify(run domain.Run, base domain.Envelope, snapshot domain.Snapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.RunID != run.ID || snapshot.AgentRef == nil {
		return domain.ErrExecutionIdentity
	}
	var record agent.Run
	if err := domain.Strict(snapshot.AgentRecord, &record); err != nil {
		return err
	}
	if record.ID != snapshot.AgentRef.RunID || record.Revision != snapshot.AgentRef.Revision {
		return domain.ErrExecutionIdentity
	}
	expected, err := Compose(run, base, record)
	if err != nil {
		return err
	}
	if expected.HistoryHash != snapshot.HistoryHash || expected.Backend != snapshot.Backend || expected.CheckpointID != snapshot.CheckpointID || expected.Base == nil || snapshot.Base == nil || !expected.Base.Equal(*snapshot.Base) {
		return domain.ErrExecutionIdentity
	}
	return nil
}
func Definition() forest.Definition {
	return forest.Definition{TypeID: "focus-go.history-model", Version: "1", Ports: []forest.PortDescriptor{forest.Port[BindingReader]("source", forest.Required(), forest.During(forest.Construction)), forest.Port[Model]("model", forest.Required())}, Capabilities: []forest.CapabilityDescriptor{forest.Capability[Model]("model", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		source, err := forest.Reference[BindingReader](env, "source")
		if err != nil {
			return nil, err
		}
		model, err := forest.Reference[Model](env, "model")
		if err != nil {
			return nil, err
		}
		return forest.Hooks{}, forest.Provide(env, "model", &historyModel{source: source, model: model})
	}}
}
