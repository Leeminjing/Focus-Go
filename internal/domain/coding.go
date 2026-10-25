package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const CodingVersion = "focus-go-context-run-v2"

type Workspace struct {
	ID          string   `json:"workspace_id"`
	Root        string   `json:"root"`
	Identity    string   `json:"directory_identity"`
	Read        []string `json:"read"`
	Write       []string `json:"write"`
	Preapproved bool     `json:"preapproved_writes"`
}

func (w Workspace) Validate() error {
	if !ID(w.ID) || w.Root == "" || !Text(w.Root) || !ID(w.Identity) || w.Read == nil || w.Write == nil {
		return ErrInvalid
	}
	for _, paths := range [][]string{w.Read, w.Write} {
		for _, p := range paths {
			if p == "" || !Text(p) {
				return ErrInvalid
			}
		}
	}
	return nil
}

type ToolDescriptor struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	Description   string          `json:"description"`
	Parameters    json.RawMessage `json:"parameters"`
	NeedsApproval bool            `json:"needs_approval"`
}
type Equipment struct {
	Model        *ModelSettings   `json:"model,omitempty"`
	Version      string           `json:"version"`
	Workspace    Workspace        `json:"workspace"`
	ModelProfile string           `json:"model_profile"`
	Tools        []ToolDescriptor `json:"tools"`
}
type ModelSettings struct {
	Endpoint        string `json:"endpoint"`
	Model           string `json:"model"`
	Instructions    string `json:"instructions"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Timeout         int64  `json:"timeout"`
}

func (e Equipment) Validate() error {
	if e.Version != CodingVersion || e.Workspace.Validate() != nil || !ID(e.ModelProfile) || len(e.Tools) != 3 {
		return ErrInvalid
	}
	if e.Model != nil {
		data, err := json.Marshal(e.Model)
		if err != nil || BytesHash(data) != e.ModelProfile || !ID(e.Model.Model) || e.Model.Endpoint == "" || e.Model.MaxOutputTokens < 16 || e.Model.MaxOutputTokens > 32768 || e.Model.Timeout < 1 {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, t := range e.Tools {
		if !ID(t.Name) || !ID(t.Version) || seen[t.Name] {
			return ErrInvalid
		}
		seen[t.Name] = true
		var args map[string]json.RawMessage
		if Strict(t.Parameters, &args) != nil || args == nil {
			return ErrInvalid
		}
	}
	for _, name := range []string{"workspace_search", "workspace_read", "workspace_edit"} {
		if !seen[name] {
			return ErrInvalid
		}
	}
	return nil
}

type Receipt struct {
	Version       string          `json:"version"`
	ID            string          `json:"receipt_id"`
	RunID         string          `json:"run_id"`
	AgentRunID    string          `json:"agent_run_id"`
	CallID        string          `json:"call_id"`
	Tool          string          `json:"tool"`
	ToolVersion   string          `json:"tool_version"`
	Arguments     json.RawMessage `json:"arguments"`
	ArgumentsHash string          `json:"arguments_hash"`
	GrantHash     string          `json:"grant_hash"`
	Authority     string          `json:"authority"`
	Path          string          `json:"path"`
	Phase         string          `json:"phase"`
	Before        string          `json:"before"`
	After         string          `json:"after"`
	BeforeHash    string          `json:"before_hash"`
	AfterHash     string          `json:"after_hash"`
	Start         int             `json:"start_byte"`
	End           int             `json:"end_byte"`
	Result        string          `json:"result"`
	Digest        string          `json:"digest"`
}

func BytesHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (r Receipt) Validate() error {
	if r.Version != CodingVersion || !ID(r.ID) || !ID(r.RunID) || !ID(r.AgentRunID) || !ID(r.CallID) || r.ToolVersion != "1" || len(r.Before) > 1<<20 || len(r.After) > 1<<20 || r.Start < 0 || r.End < r.Start || r.End > len(r.Before) {
		return ErrCorrupt
	}
	if r.Tool != "workspace_search" && r.Tool != "workspace_read" && r.Tool != "workspace_edit" {
		return ErrCorrupt
	}
	if r.Phase != "prepared" && r.Phase != "completed" && r.Phase != "operator_reconciled" {
		return ErrCorrupt
	}
	hash, err := HashJSON(r.Arguments)
	if err != nil || hash != r.ArgumentsHash {
		return ErrHash
	}
	if r.BeforeHash != "" && r.BeforeHash != BytesHash([]byte(r.Before)) {
		return ErrHash
	}
	if r.AfterHash != "" && r.AfterHash != BytesHash([]byte(r.After)) {
		return ErrHash
	}
	digest := r.Digest
	r.Digest = ""
	expected, err := Hash(r)
	if err != nil || expected != digest {
		return ErrHash
	}
	return nil
}

type Artifact struct {
	Format string `json:"format"`
	Digest string `json:"sha256"`
	Data   []byte `json:"data"`
}
type Evidence struct {
	Version      string     `json:"version"`
	Equipment    Equipment  `json:"equipment"`
	Receipts     []Receipt  `json:"receipts"`
	ModelRecords []Artifact `json:"model_records"`
}

func (e Evidence) Validate() error {
	if e.Version != CodingVersion || e.Equipment.Validate() != nil || e.Receipts == nil || len(e.Receipts) > 64 || e.ModelRecords == nil {
		return ErrCorrupt
	}
	if e.Equipment.Model != nil && len(e.ModelRecords) == 0 {
		return ErrCorrupt
	}
	bytes := 0
	seen := map[string]bool{}
	for _, r := range e.Receipts {
		if r.Validate() != nil || r.Phase == "prepared" || seen[r.ID] {
			return ErrCorrupt
		}
		seen[r.ID] = true
		bytes += len(r.Before) + len(r.After) + len(r.Result)
	}
	if bytes > 8<<20 {
		return ErrInvalid
	}
	for _, a := range e.ModelRecords {
		if a.Format != "openai-responses-journal-v1" || a.Digest != BytesHash(a.Data) || len(a.Data) > 8<<20 {
			return ErrCorrupt
		}
	}
	return nil
}
