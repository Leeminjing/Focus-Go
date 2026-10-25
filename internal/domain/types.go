package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const ContractVersion = "focus-go-context-run-v1"
const HashProfile = "focus-json-integer-v1"
const SelectionVersion = "focus-semantic-selection-v2"

type Ref struct {
	ContextID         string  `json:"context_id"`
	RevisionID        string  `json:"revision_id"`
	Generation        uint64  `json:"generation"`
	ExecutionThreadID string  `json:"execution_thread_id"`
	CheckpointNS      string  `json:"checkpoint_ns"`
	CheckpointID      *string `json:"checkpoint_id"`
	PayloadMode       string  `json:"payload_mode"`
}

func Pointer(s string) *string { return &s }
func (r Ref) Equal(other Ref) bool {
	return r.ContextID == other.ContextID && r.RevisionID == other.RevisionID && r.Generation == other.Generation && r.ExecutionThreadID == other.ExecutionThreadID && r.CheckpointNS == other.CheckpointNS && r.PayloadMode == other.PayloadMode && ((r.CheckpointID == nil && other.CheckpointID == nil) || (r.CheckpointID != nil && other.CheckpointID != nil && *r.CheckpointID == *other.CheckpointID))
}
func (r Ref) Validate() error {
	if !ID(r.ContextID) || !ID(r.RevisionID) || !ID(r.ExecutionThreadID) || r.Generation < 1 || r.Generation > uint64(MaxInteger) || !Text(r.CheckpointNS) || len(r.CheckpointNS) > 256 {
		return ErrInvalid
	}
	if r.PayloadMode != "checkpoint" && r.PayloadMode != "definition" {
		return ErrInvalid
	}
	if r.CheckpointID != nil && !ID(*r.CheckpointID) {
		return ErrInvalid
	}
	if r.PayloadMode == "checkpoint" && r.CheckpointID == nil {
		return ErrInvalid
	}
	return nil
}
func ID(s string) bool {
	return len(s) > 0 && len(s) <= 256 && utf8.ValidString(s) && strings.TrimSpace(s) == s
}
func Text(s string) bool { return len(s) <= 1<<20 && utf8.ValidString(s) }
func Null(data json.RawMessage) bool {
	return len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

type Backend struct {
	Engine  string `json:"engine"`
	Format  string `json:"format"`
	StoreID string `json:"store_id"`
}

func (b Backend) Validate() error {
	if b.Engine != "focus-go-snapshot" || b.Format != "context-snapshot-v1" || !ID(b.StoreID) {
		return ErrUnsupportedCheckpoint
	}
	return nil
}

type Source struct {
	Source   Ref `json:"source"`
	Position int `json:"position"`
}
type Call struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	Type string          `json:"type,omitempty"`
}
type Message struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Calls   []Call `json:"tool_calls,omitempty"`
	CallID  string `json:"tool_call_id,omitempty"`
}
type Item struct {
	ItemID            string            `json:"item_id"`
	Kind              string            `json:"kind"`
	Origin            string            `json:"origin"`
	Scope             string            `json:"scope"`
	Payload           json.RawMessage   `json:"payload"`
	SourceRefs        []json.RawMessage `json:"source_refs"`
	MessageID         *string           `json:"message_id"`
	Provider          *string           `json:"provider"`
	ProjectionVersion *string           `json:"projection_version"`
	Bridge            json.RawMessage   `json:"bridge"`
}
type History struct {
	SchemaVersion    int               `json:"schema_version"`
	Authored         []Item            `json:"authored_items"`
	Execution        []Item            `json:"execution_items"`
	BindingRefs      []json.RawMessage `json:"binding_refs"`
	SelectionVersion string            `json:"selection_version"`
}
type Revision struct {
	Ref               Ref               `json:"ref"`
	Sources           []Source          `json:"sources"`
	History           History           `json:"history_payload"`
	ProjectionStatus  string            `json:"projection_status"`
	OriginKind        string            `json:"origin_kind"`
	OriginID          *string           `json:"origin_id"`
	CreatedAt         string            `json:"created_at"`
	ContentHash       string            `json:"content_hash"`
	DefinitionHash    *string           `json:"definition_hash"`
	ProjectionHash    *string           `json:"projection_hash"`
	InitialMessageIDs []string          `json:"initial_message_ids"`
	RepairManifest    []json.RawMessage `json:"repair_manifest"`
	Issues            []json.RawMessage `json:"issues"`
}
type Envelope struct {
	EvidenceHash string   `json:"evidence_hash,omitempty"`
	Version      string   `json:"contract_version"`
	RecordType   string   `json:"record_type"`
	HashProfile  string   `json:"hash_profile"`
	Backend      Backend  `json:"checkpoint_backend"`
	Revision     Revision `json:"revision"`
}
type Context struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"context_id"`
	Lifecycle   string `json:"lifecycle"`
	Current     Ref    `json:"current_ref"`
}
type Fixture struct {
	Version  string   `json:"contract_version"`
	Context  Context  `json:"context"`
	Revision Envelope `json:"revision"`
}

func (i Item) Message() (Message, error) {
	if !ID(i.ItemID) || i.MessageID == nil || !ID(*i.MessageID) || i.SourceRefs == nil {
		return Message{}, ErrInvalid
	}
	for _, raw := range i.SourceRefs {
		var source map[string]json.RawMessage
		if Strict(raw, &source) != nil || source == nil {
			return Message{}, ErrInvalid
		}
	}
	if i.Kind != "message" && i.Kind != "function_call_output" {
		return Message{}, ErrUnsupportedHistory
	}
	if i.Scope != "revision" && i.Scope != "execution" {
		return Message{}, ErrUnsupportedHistory
	}
	if i.Bridge != nil && !bytes.Equal(bytes.TrimSpace(i.Bridge), []byte("null")) {
		return Message{}, ErrUnsupportedHistory
	}
	switch i.Origin {
	case "direct_user", "user_authored", "curator", "provider", "tool", "legacy_unknown":
	default:
		return Message{}, ErrUnsupportedHistory
	}
	var payload struct {
		Message Message `json:"message"`
	}
	if err := Strict(i.Payload, &payload); err != nil {
		return Message{}, fmt.Errorf("%w: message payload: %v", ErrUnsupportedHistory, err)
	}
	m := payload.Message
	if !ID(m.ID) || m.ID != *i.MessageID || !Text(m.Content) {
		return Message{}, ErrInvalid
	}
	switch m.Role {
	case "human", "user", "ai", "assistant", "system", "tool":
	default:
		return Message{}, ErrUnsupportedHistory
	}
	if i.Origin == "direct_user" && (m.Role != "human" && m.Role != "user") {
		return Message{}, ErrInvalid
	}
	if len(m.Calls) > 0 && (m.Role != "ai" && m.Role != "assistant") {
		return Message{}, ErrInvalid
	}
	if len(m.Calls) > 0 && i.Origin != "provider" {
		return Message{}, ErrUnsupportedHistory
	}
	if m.Role == "tool" && (i.Origin != "tool" || i.Kind != "function_call_output" || !ID(m.CallID)) {
		return Message{}, ErrUnsupportedHistory
	}
	if m.Role != "tool" && (m.CallID != "" || i.Kind != "message") {
		return Message{}, ErrInvalid
	}
	for _, call := range m.Calls {
		if !ID(call.ID) || !ID(call.Name) || (call.Type != "" && call.Type != "tool_call") {
			return Message{}, ErrInvalid
		}
		var arguments map[string]json.RawMessage
		if Strict(call.Args, &arguments) != nil || arguments == nil {
			return Message{}, ErrInvalid
		}
	}
	return m, nil
}
func inventory(items []Item, authored bool) ([]Message, error) {
	if items == nil || len(items) > 4096 {
		return nil, ErrInvalid
	}
	ids := map[string]bool{}
	messages := map[string]bool{}
	calls := map[string]bool{}
	outputs := map[string]bool{}
	result := make([]Message, 0, len(items))
	for _, item := range items {
		m, err := item.Message()
		if err != nil {
			return nil, err
		}
		if ids[item.ItemID] || messages[m.ID] {
			return nil, ErrInvalid
		}
		ids[item.ItemID] = true
		messages[m.ID] = true
		if authored && (item.Origin == "provider" || item.Origin == "tool") {
			return nil, ErrUnsupportedHistory
		}
		for _, call := range m.Calls {
			if calls[call.ID] {
				return nil, ErrInvalid
			}
			calls[call.ID] = true
		}
		if m.Role == "tool" {
			if !calls[m.CallID] || outputs[m.CallID] {
				return nil, ErrInvalid
			}
			outputs[m.CallID] = true
		}
		result = append(result, m)
	}
	for id := range calls {
		if !outputs[id] {
			return nil, ErrInvalid
		}
	}
	return result, nil
}
func (h History) Validate() error {
	if h.SchemaVersion != 2 || h.SelectionVersion != SelectionVersion || h.BindingRefs == nil {
		return ErrUnsupportedHistory
	}
	if len(h.BindingRefs) != 0 {
		return ErrUnsupportedHistory
	}
	if _, err := inventory(h.Authored, true); err != nil {
		return err
	}
	_, err := inventory(h.Execution, false)
	return err
}
func (h History) Messages() ([]Message, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	return inventory(h.Execution, false)
}
func (e Envelope) ExpectedHash() (string, error) {
	r := e.Revision
	if r.Ref.CheckpointID == nil {
		return "", ErrInvalid
	}
	if e.Version == CodingVersion {
		if r.OriginKind != "run_settled" || r.OriginID == nil || len(e.EvidenceHash) != 64 {
			return "", ErrInvalid
		}
		return Hash(struct {
			RunID        string  `json:"run_id"`
			CheckpointID string  `json:"checkpoint_id"`
			History      History `json:"history_payload"`
			Evidence     string  `json:"evidence_hash"`
		}{*r.OriginID, *r.Ref.CheckpointID, r.History, e.EvidenceHash})
	}
	if r.OriginKind == "root" {
		return Hash(struct {
			ContextID    string  `json:"context_id"`
			CheckpointID string  `json:"checkpoint_id"`
			History      History `json:"history_payload"`
		}{r.Ref.ContextID, *r.Ref.CheckpointID, r.History})
	}
	if r.OriginKind == "run_settled" && r.OriginID != nil {
		return Hash(struct {
			RunID        string  `json:"run_id"`
			CheckpointID string  `json:"checkpoint_id"`
			History      History `json:"history_payload"`
		}{*r.OriginID, *r.Ref.CheckpointID, r.History})
	}
	return "", ErrUnsupportedHistory
}
func (e Envelope) Validate() error {
	if (e.Version != ContractVersion && e.Version != CodingVersion) || e.RecordType != "context_revision" || e.HashProfile != HashProfile || (e.Version == ContractVersion && e.EvidenceHash != "") {
		return ErrInvalid
	}
	if err := e.Backend.Validate(); err != nil {
		return err
	}
	r := e.Revision
	if err := r.Ref.Validate(); err != nil {
		return err
	}
	if r.Ref.PayloadMode != "checkpoint" || r.Ref.CheckpointID == nil {
		return ErrUnsupportedCheckpoint
	}
	if r.ProjectionStatus != "valid" || r.Sources == nil {
		return ErrUnsupportedHistory
	}
	for _, value := range []*string{r.DefinitionHash, r.ProjectionHash} {
		if value != nil {
			if len(*value) != 64 {
				return ErrHash
			}
			for _, digit := range *value {
				if !(digit >= '0' && digit <= '9') && !(digit >= 'a' && digit <= 'f') {
					return ErrHash
				}
			}
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err != nil {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for pos, source := range r.Sources {
		if source.Position != pos || source.Source.Validate() != nil || source.Source.CheckpointID == nil || seen[source.Source.RevisionID] || source.Source.RevisionID == r.Ref.RevisionID {
			return ErrInvalid
		}
		seen[source.Source.RevisionID] = true
	}
	if r.OriginKind == "root" && len(r.Sources) != 0 {
		return ErrInvalid
	}
	if r.OriginKind == "run_settled" && (r.OriginID == nil || !ID(*r.OriginID) || len(r.Sources) != 1 || r.Sources[0].Source.ContextID != r.Ref.ContextID || r.Sources[0].Source.Generation >= r.Ref.Generation) {
		return ErrInvalid
	}
	if len(r.RepairManifest) != 0 || len(r.Issues) != 0 {
		return ErrUnsupportedHistory
	}
	if err := r.History.Validate(); err != nil {
		return err
	}
	actual, err := e.ExpectedHash()
	if err != nil {
		return err
	}
	if actual != r.ContentHash {
		return ErrHash
	}
	return nil
}
func (f Fixture) Validate() error {
	if f.Version != ContractVersion || !ID(f.Context.WorkspaceID) || !ID(f.Context.ID) || f.Context.Lifecycle != "active" || !f.Context.Current.Equal(f.Revision.Revision.Ref) || f.Context.ID != f.Context.Current.ContextID {
		return ErrInvalid
	}
	return f.Revision.Validate()
}
func NewItem(message Message, origin, scope string, sourceRefs []json.RawMessage) (Item, error) {
	payload, err := Marshal(struct {
		Message Message `json:"message"`
	}{message})
	if err != nil {
		return Item{}, err
	}
	return Item{ItemID: message.ID, MessageID: Pointer(message.ID), Kind: map[bool]string{true: "function_call_output", false: "message"}[message.Role == "tool"], Origin: origin, Scope: scope, Payload: payload, SourceRefs: sourceRefs}, nil
}
