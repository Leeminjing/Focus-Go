package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

type Request struct {
	WorkspaceID string `json:"workspace_id"`
	ContextID   string `json:"context_id"`
	Expected    Ref    `json:"expected_ref"`
	Input       string `json:"input"`
	Key         string `json:"idempotency_key"`
}

func (r Request) Validate() error {
	if !ID(r.WorkspaceID) || !ID(r.ContextID) || !ID(r.Key) || r.Input == "" || !Text(r.Input) || r.ContextID != r.Expected.ContextID {
		return ErrInvalid
	}
	return r.Expected.Validate()
}
func StableID(kind, id string, index int) string {
	sum := sha256.Sum256([]byte("focus-go:" + kind + ":" + id + ":" + strconv.Itoa(index)))
	return kind + "-" + hex.EncodeToString(sum[:])
}
func RequestHash(r Request, profile string) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return Hash(struct {
		Request Request `json:"request"`
		Profile string  `json:"model_profile"`
	}{r, profile})
}

type Settlement struct {
	RunID             string  `json:"run_id"`
	TaskID            string  `json:"task_id"`
	Kind              string  `json:"kind"`
	Status            string  `json:"status"`
	FinalCheckpointID *string `json:"final_checkpoint_id"`
	ContextRevision   *Ref    `json:"context_revision"`
	Publication       string  `json:"context_publication"`
	EventID           string  `json:"event_id"`
	Idempotent        bool    `json:"idempotent"`
}
type Run struct {
	ID                string      `json:"run_id"`
	TaskID            string      `json:"task_id"`
	WorkspaceID       string      `json:"workspace_id"`
	Kind              string      `json:"kind"`
	Status            string      `json:"status"`
	Base              Ref         `json:"base_ref"`
	Input             []Message   `json:"input_messages"`
	Origin            string      `json:"origin"`
	OriginMessageID   string      `json:"origin_message_id"`
	Key               string      `json:"idempotency_key"`
	RequestHash       string      `json:"request_hash"`
	ExecutionThreadID string      `json:"execution_thread_id"`
	CheckpointNS      string      `json:"checkpoint_ns"`
	AgentRunID        string      `json:"agent_run_id"`
	Phase             string      `json:"execution_phase"`
	Reason            string      `json:"reason"`
	AgentRevision     uint64      `json:"agent_checkpoint_revision"`
	ModelProfile      string      `json:"model_profile"`
	InputMessageIDs   []string    `json:"input_message_ids"`
	ResultRef         *string     `json:"result_ref"`
	Settlement        *Settlement `json:"settlement"`
	SettledAt         *string     `json:"settled_at"`
}

func (r Run) Validate() error {
	if !ID(r.ID) || !ID(r.TaskID) || !ID(r.WorkspaceID) || r.Kind != "main" || r.Origin != "direct_user" || len(r.Input) != 1 || r.Base.Validate() != nil || r.TaskID != r.Base.ContextID || r.ExecutionThreadID != r.Base.ExecutionThreadID || r.CheckpointNS != r.Base.CheckpointNS || r.AgentRunID != StableID("agent", r.ID, 0) || r.OriginMessageID != StableID("message", r.ID, 0) || r.Input[0].ID != r.OriginMessageID || r.Input[0].Role != "user" || len(r.Input[0].Calls) != 0 || r.Input[0].CallID != "" || r.Input[0].Content == "" || !Text(r.Input[0].Content) || !ID(r.ModelProfile) || !ID(r.Key) {
		return ErrCorrupt
	}
	switch r.Status {
	case "pending", "running", "success", "error", "timeout", "interrupted":
	default:
		return ErrCorrupt
	}
	if !Text(r.Reason) {
		return ErrCorrupt
	}
	switch r.Phase {
	case "admitted", "model-ready", "model-running", "tools-ready", "tool-running", "awaiting-approval", "outcome-unknown", "completed", "canceled", "failed":
	default:
		return ErrCorrupt
	}
	expected, err := RequestHash(Request{WorkspaceID: r.WorkspaceID, ContextID: r.TaskID, Expected: r.Base, Input: r.Input[0].Content, Key: r.Key}, r.ModelProfile)
	if err != nil || expected != r.RequestHash {
		return ErrCorrupt
	}
	if (r.Settlement == nil) != (r.SettledAt == nil) {
		return ErrCorrupt
	}
	if r.Settlement != nil {
		settled := r.Settlement
		if settled.RunID != r.ID || settled.TaskID != r.TaskID || settled.Kind != "main" || settled.Status != r.Status || settled.EventID != StableID("event", r.ID, 0) || settled.Idempotent {
			return ErrCorrupt
		}
		switch settled.Publication {
		case "published":
			if r.Status != "success" || settled.ContextRevision == nil || settled.FinalCheckpointID == nil {
				return ErrCorrupt
			}
		case "superseded":
			if r.Status != "success" || settled.ContextRevision != nil || settled.FinalCheckpointID == nil {
				return ErrCorrupt
			}
		case "not_applicable":
			if r.Status == "success" || settled.ContextRevision != nil {
				return ErrCorrupt
			}
		default:
			return ErrCorrupt
		}
	}
	return nil
}

type AgentCheckpointRef struct {
	Engine   string `json:"engine"`
	Format   string `json:"format"`
	StoreID  string `json:"store_id"`
	RunID    string `json:"agent_run_id"`
	Revision uint64 `json:"revision"`
}
type Snapshot struct {
	Version           string              `json:"contract_version"`
	CheckpointID      string              `json:"checkpoint_id"`
	Backend           Backend             `json:"checkpoint_backend"`
	ContextID         string              `json:"context_id"`
	ExecutionThreadID string              `json:"execution_thread_id"`
	CheckpointNS      string              `json:"checkpoint_ns"`
	RunID             string              `json:"run_id"`
	Base              *Ref                `json:"base_ref"`
	AgentRef          *AgentCheckpointRef `json:"agent_checkpoint"`
	AgentRecord       json.RawMessage     `json:"agent_record"`
	History           History             `json:"history_payload"`
	HistoryHash       string              `json:"history_hash"`
	CreatedAt         string              `json:"created_at"`
}

func (s Snapshot) Validate() error {
	if s.Version != ContractVersion || !ID(s.CheckpointID) || !ID(s.ContextID) || !ID(s.ExecutionThreadID) || !Text(s.CheckpointNS) {
		return ErrCorrupt
	}
	if _, err := time.Parse(time.RFC3339Nano, s.CreatedAt); err != nil {
		return ErrCorrupt
	}
	if err := s.Backend.Validate(); err != nil {
		return err
	}
	if err := s.History.Validate(); err != nil {
		return err
	}
	hash, err := Hash(s.History)
	if err != nil || hash != s.HistoryHash {
		return ErrHash
	}
	if s.RunID == "" {
		if s.Base != nil || s.AgentRef != nil || !Null(s.AgentRecord) {
			return ErrCorrupt
		}
	} else {
		if !ID(s.RunID) || s.Base == nil || s.Base.Validate() != nil || s.Base.ContextID != s.ContextID || s.Base.ExecutionThreadID != s.ExecutionThreadID || s.Base.CheckpointNS != s.CheckpointNS || s.AgentRef == nil {
			return ErrCorrupt
		}
		a := s.AgentRef
		if a.Engine != "go-forest-agent" || a.Format != "run-v1" || !ID(a.StoreID) || a.RunID != StableID("agent", s.RunID, 0) || a.Revision == 0 || len(s.AgentRecord) == 0 {
			return ErrUnsupportedCheckpoint
		}
		if _, err := Canonical(s.AgentRecord); err != nil {
			return err
		}
	}
	return nil
}
func (s Snapshot) Matches(e Envelope) error {
	r := e.Revision.Ref
	if r.CheckpointID == nil || s.CheckpointID != *r.CheckpointID || s.ContextID != r.ContextID || s.ExecutionThreadID != r.ExecutionThreadID || s.CheckpointNS != r.CheckpointNS || s.Backend != e.Backend {
		return ErrRefMismatch
	}
	if e.Revision.OriginKind == "root" && s.RunID != "" {
		return ErrRefMismatch
	}
	if e.Revision.OriginKind == "run_settled" && (e.Revision.OriginID == nil || s.RunID != *e.Revision.OriginID || s.Base == nil || len(e.Revision.Sources) != 1 || !s.Base.Equal(e.Revision.Sources[0].Source)) {
		return ErrRefMismatch
	}
	hash, err := Hash(e.Revision.History)
	if err != nil || hash != s.HistoryHash {
		return ErrHash
	}
	return s.Validate()
}

type Event struct {
	ID         string     `json:"event_id"`
	Kind       string     `json:"kind"`
	RunID      string     `json:"run_id"`
	Settlement Settlement `json:"settlement"`
}
type Catalog struct {
	Version   string              `json:"contract_version"`
	Contexts  map[string]Context  `json:"contexts"`
	Revisions map[string]Envelope `json:"revisions"`
	Runs      map[string]Run      `json:"runs"`
	Events    map[string]Event    `json:"outbox"`
}

func EmptyCatalog() Catalog {
	return Catalog{Version: ContractVersion, Contexts: map[string]Context{}, Revisions: map[string]Envelope{}, Runs: map[string]Run{}, Events: map[string]Event{}}
}
func (c Catalog) Validate() error {
	if c.Version != ContractVersion || c.Contexts == nil || c.Revisions == nil || c.Runs == nil || c.Events == nil {
		return ErrCorrupt
	}
	for id, env := range c.Revisions {
		if env.Revision.Ref.RevisionID != id {
			return ErrCorrupt
		}
		if err := env.Validate(); err != nil {
			return err
		}
		for _, source := range env.Revision.Sources {
			previous, ok := c.Revisions[source.Source.RevisionID]
			if !ok || !previous.Revision.Ref.Equal(source.Source) {
				return ErrRefMismatch
			}
		}
	}
	for id, context := range c.Contexts {
		env, ok := c.Revisions[context.Current.RevisionID]
		if context.ID != id || !ID(context.WorkspaceID) || context.Lifecycle != "active" || !ok || !env.Revision.Ref.Equal(context.Current) || context.Current.ContextID != id {
			return ErrCorrupt
		}
	}
	active := map[string]bool{}
	keys := map[struct{ workspace, key string }]bool{}
	for id, run := range c.Runs {
		if run.ID != id || run.Validate() != nil {
			return ErrCorrupt
		}
		context, ok := c.Contexts[run.TaskID]
		base, exists := c.Revisions[run.Base.RevisionID]
		if !ok || context.WorkspaceID != run.WorkspaceID || !exists || !base.Revision.Ref.Equal(run.Base) {
			return ErrCorrupt
		}
		messages, err := base.Revision.History.Messages()
		if err != nil || len(run.InputMessageIDs) != len(messages)+1 {
			return ErrCorrupt
		}
		for i, message := range messages {
			if run.InputMessageIDs[i] != message.ID {
				return ErrCorrupt
			}
		}
		if run.InputMessageIDs[len(messages)] != run.OriginMessageID {
			return ErrCorrupt
		}
		key := struct{ workspace, key string }{run.WorkspaceID, run.Key}
		if keys[key] {
			return ErrCorrupt
		}
		keys[key] = true
		if run.Settlement == nil {
			if active[run.TaskID] {
				return ErrCorrupt
			}
			active[run.TaskID] = true
		} else {
			event, ok := c.Events[run.Settlement.EventID]
			if !ok || event.RunID != run.ID {
				return ErrCorrupt
			}
			if run.Settlement.ContextRevision != nil {
				e, ok := c.Revisions[run.Settlement.ContextRevision.RevisionID]
				if !ok || !e.Revision.Ref.Equal(*run.Settlement.ContextRevision) {
					return ErrCorrupt
				}
			}
		}
	}
	for id, event := range c.Events {
		run, ok := c.Runs[event.RunID]
		if id != event.ID || event.Kind != "MainRunSettled" || !ok || run.Settlement == nil {
			return ErrCorrupt
		}
		a, _ := Hash(event.Settlement)
		b, _ := Hash(*run.Settlement)
		if a != b {
			return ErrCorrupt
		}
	}
	return nil
}
