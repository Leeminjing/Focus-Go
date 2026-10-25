package app

import (
	"context"
	"encoding/json"
	"github.com/Leeminjing/Focus-Go/internal/agentbridge"
	"strings"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest-agent/trees/events"
	"github.com/Leeminjing/go-forest-agent/trees/openairesponses"
	"github.com/Leeminjing/go-forest/forest"
)

func CodingEquipment(binding domain.Workspace, profile string) (domain.Equipment, error) {
	e := domain.Equipment{Version: domain.CodingVersion, Workspace: binding, ModelProfile: profile, Tools: []domain.ToolDescriptor{}}
	for _, name := range []string{"workspace_search", "workspace_read", "workspace_edit"} {
		spec, err := workspace.Schema(name, binding.Preapproved)
		if err != nil {
			return e, err
		}
		e.Tools = append(e.Tools, domain.ToolDescriptor{Name: spec.Name, Version: spec.Version, Description: spec.Description, Parameters: spec.Parameters, NeedsApproval: spec.NeedsApproval})
	}
	return e, e.Validate()
}

type eventSink struct {
	memory  *events.Memory
	handler func(context.Context, agent.Event) error
}

func (s *eventSink) Publish(ctx context.Context, event agent.Event) error {
	if err := s.memory.Publish(ctx, event); err != nil {
		return err
	}
	if s.handler != nil {
		return s.handler(ctx, event)
	}
	return nil
}
func eventDefinition(handler func(context.Context, agent.Event) error) forest.Definition {
	return forest.Definition{TypeID: "focus.events", Version: "1", Capabilities: []forest.CapabilityDescriptor{forest.Capability[agent.Events]("events", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		return forest.Hooks{}, forest.Provide(env, "events", &eventSink{events.New(32768), handler})
	}}
}

type evidenceReader interface {
	Collect(context.Context, domain.Run, domain.Envelope, agent.Run) (domain.Evidence, error)
}
type evidenceCollector struct {
	workspace forest.Ref[workspace.EvidenceAccess]
	journal   forest.Ref[openairesponses.Journal]
	native    bool
}

func (c *evidenceCollector) Collect(ctx context.Context, run domain.Run, base domain.Envelope, record agent.Run) (domain.Evidence, error) {
	if run.Equipment == nil {
		return domain.Evidence{}, domain.ErrInvalid
	}
	e := domain.Evidence{Version: domain.CodingVersion, Equipment: *run.Equipment, Receipts: []domain.Receipt{}, ModelRecords: []domain.Artifact{}}
	var receipts []domain.Receipt
	if err := c.workspace.Use(ctx, func(w workspace.EvidenceAccess) error {
		var err error
		receipts, err = w.Receipts(ctx, record.ID)
		return err
	}); err != nil {
		return e, err
	}
	byCall := map[string]domain.Receipt{}
	for _, r := range receipts {
		if _, exists := byCall[r.CallID]; exists {
			return e, domain.ErrCorrupt
		}
		byCall[r.CallID] = r
	}
	for _, m := range record.Messages {
		if m.Role == "tool" {
			receipt, ok := byCall[m.CallID]
			if !ok {
				return e, domain.ErrCorrupt
			}
			e.Receipts = append(e.Receipts, receipt)
			delete(byCall, m.CallID)
		}
	}
	if len(byCall) > 0 {
		return e, domain.ErrCorrupt
	}
	if c.native {
		var records []openairesponses.Record
		if err := c.journal.Use(ctx, func(j openairesponses.Journal) error {
			var err error
			records, err = j.Load(ctx, record.ID)
			return err
		}); err != nil {
			return e, err
		}
		if len(records) != record.ModelTurns {
			return e, domain.ErrExecutionIdentity
		}
		for _, r := range records {
			data, err := json.Marshal(r)
			if err != nil {
				return e, err
			}
			e.ModelRecords = append(e.ModelRecords, domain.Artifact{Format: r.Version, Digest: domain.BytesHash(data), Data: data})
		}
	}
	return e, verifyEvidence(run, base, record, e)
}
func verifyEvidence(run domain.Run, base domain.Envelope, record agent.Run, e domain.Evidence) error {
	if run.Equipment == nil || e.Validate() != nil || record.ID != run.AgentRunID {
		return domain.ErrCorrupt
	}
	a, _ := domain.Hash(*run.Equipment)
	b, _ := domain.Hash(e.Equipment)
	if a != b {
		return domain.ErrExecutionIdentity
	}
	grant, _ := domain.Hash(e.Equipment.Workspace)
	calls := map[string]agent.ToolCall{}
	for _, m := range record.Messages {
		for _, call := range m.Calls {
			calls[call.ID] = call
		}
	}
	index := 0
	last := map[string]string{}
	approved := map[string]bool{}
	for _, event := range record.Events {
		if event.Kind == "tool.approval" && event.Text == string(agent.Granted) {
			approved[event.CallID] = true
		}
	}
	for _, m := range record.Messages {
		if m.Role != "tool" {
			continue
		}
		if index >= len(e.Receipts) {
			return domain.ErrCorrupt
		}
		r := e.Receipts[index]
		index++
		call, ok := calls[m.CallID]
		hash, err := domain.HashJSON(call.Arguments)
		if !ok || err != nil || r.RunID != run.ID || r.AgentRunID != record.ID || r.CallID != m.CallID || r.Tool != call.Name || r.ArgumentsHash != hash || r.GrantHash != grant || r.Result != m.Content || r.ToolVersion != "1" || r.Phase == "prepared" {
			return domain.ErrExecutionIdentity
		}
		if r.Tool == "workspace_edit" && (strings.Contains(r.Result, `"status":"applied"`) || strings.Contains(r.Result, `"status":"reconciled"`)) {
			if !e.Equipment.Workspace.Preapproved && !approved[r.CallID] {
				return domain.ErrExecutionIdentity
			}
			if previous := last[r.Path]; previous != "" && previous != r.BeforeHash {
				return domain.ErrExecutionIdentity
			}
			last[r.Path] = r.AfterHash
		}
	}
	if index != len(e.Receipts) {
		return domain.ErrCorrupt
	}
	if len(e.ModelRecords) > 0 {
		previous, err := base.Revision.History.Messages()
		if err != nil {
			return err
		}
		prefix := []agent.Message{}
		for _, m := range previous {
			prefix = append(prefix, agentbridge.ToAgent(m))
		}
		records := []openairesponses.Record{}
		for _, artifact := range e.ModelRecords {
			var r openairesponses.Record
			if json.Unmarshal(artifact.Data, &r) != nil {
				return domain.ErrCorrupt
			}
			records = append(records, r)
		}
		if openairesponses.CheckChain(record.ID, records) != nil || len(records) != record.ModelTurns {
			return domain.ErrCorrupt
		}
		for _, r := range records {
			if r.Profile != run.ModelProfile {
				return domain.ErrExecutionIdentity
			}
		}
		turn := 0
		for index, message := range record.Messages {
			if message.Role != "assistant" {
				continue
			}
			if turn >= len(records) {
				return domain.ErrCorrupt
			}
			native := records[turn]
			turn++
			expected := append(append([]agent.Message{}, prefix...), record.Messages[:index]...)
			want, _ := domain.Hash(expected)
			got, _ := domain.Hash(native.Request.Messages)
			if want != got {
				return domain.ErrExecutionIdentity
			}
			want, _ = domain.Hash(agent.ModelResult{Content: message.Content, Calls: message.Calls})
			got, _ = domain.Hash(native.Result)
			if want != got {
				return domain.ErrExecutionIdentity
			}
			want, _ = domain.Hash(e.Equipment.Tools)
			got, _ = domain.Hash(native.Request.Tools)
			if want != got {
				return domain.ErrExecutionIdentity
			}
		}
	}
	return nil
}
func evidenceDefinition(native bool) forest.Definition {
	return forest.Definition{TypeID: "focus.evidence", Version: "1", Ports: []forest.PortDescriptor{forest.Port[workspace.EvidenceAccess]("workspace", forest.Required()), forest.Port[openairesponses.Journal]("journal")}, Capabilities: []forest.CapabilityDescriptor{forest.Capability[evidenceReader]("evidence", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		w, err := forest.Reference[workspace.EvidenceAccess](env, "workspace")
		if err != nil {
			return nil, err
		}
		j, err := forest.Reference[openairesponses.Journal](env, "journal")
		if err != nil {
			return nil, err
		}
		return forest.Hooks{}, forest.Provide(env, "evidence", &evidenceCollector{w, j, native})
	}}
}
