package workspace_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
)

func TestToolsReadVersionEditAndKeepDurableEvidence(t *testing.T) {
	root := t.TempDir()
	original := "package calc\r\nfunc Add(a,b int) int { return a-b }\r\n"
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w", root, []string{"calc.go"}, []string{"calc.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	resource, err := workspace.New(binding, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.Close()
	authorize := func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus-run", Workspace: binding, Approved: true, AgentRevision: 2}, nil
	}
	read := workspace.NewTool("workspace_read", resource, authorize)
	edit := workspace.NewTool("workspace_edit", resource, authorize)
	invoke := func(tool agent.Tool, id string, args any) map[string]json.RawMessage {
		t.Helper()
		raw, _ := domain.Marshal(args)
		result, err := tool.Execute(context.Background(), agent.ToolRequest{RunID: "agent-run", Call: agent.ToolCall{ID: id, Name: mustSpec(t, tool).Name, Arguments: raw}})
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]json.RawMessage
		if domain.Strict([]byte(result), &output) != nil {
			t.Fatal(result)
		}
		return output
	}
	output := invoke(read, "read-1", map[string]any{"path": "calc.go", "start_line": 1, "line_count": 200})
	var digest, receipt string
	_ = json.Unmarshal(output["sha256"], &digest)
	_ = json.Unmarshal(output["read_receipt_id"], &receipt)
	if digest != domain.BytesHash([]byte(original)) || receipt == "" {
		t.Fatal(output)
	}
	args := map[string]any{"path": "calc.go", "expected_sha256": digest, "read_receipt_id": receipt, "old_text": "return a-b", "new_text": "return a+b"}
	changed := invoke(edit, "edit-1", args)
	if string(changed["status"]) != `"applied"` {
		t.Fatal(changed)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(actual) != "package calc\r\nfunc Add(a,b int) int { return a+b }\r\n" {
		t.Fatal(string(actual))
	}
	// Reopen durable resources and replay the same identity after a user change.
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	resource, err = workspace.New(binding, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.Close()
	edit = workspace.NewTool("workspace_edit", resource, authorize)
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte("user changed later\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again := invoke(edit, "edit-1", args)
	if string(again["edit_receipt_id"]) != string(changed["edit_receipt_id"]) {
		t.Fatal(again)
	}
	actual, _ = os.ReadFile(filepath.Join(root, "calc.go"))
	if string(actual) != "user changed later\n" {
		t.Fatal("replay wrote old content")
	}
	receipts, err := resource.Receipts(context.Background(), "agent-run")
	if err != nil || len(receipts) != 2 {
		t.Fatal(receipts, err)
	}
	for _, r := range receipts {
		if r.Validate() != nil {
			t.Fatal(r)
		}
	}
}

func TestWorkspaceRejectsUnapprovedEditsAndUnsafePathsWithoutChangingFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "source.go"), []byte("old\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("private"), 0600)
	binding, err := workspace.Bind("w", root, []string{"."}, []string{"source.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workspace.New(binding, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	authorize := func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 2}, nil
	}
	read := workspace.NewTool("workspace_read", w, authorize)
	for i, path := range []string{"../outside", "/absolute", `C:/root`, `.env`, `.git/config`, `NUL`, `file.go:stream`, `folder\\file.go`} {
		raw, _ := domain.Marshal(map[string]any{"path": path, "start_line": 1, "line_count": 10})
		result, err := read.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: domain.StableID("call", "unsafe", i), Name: "workspace_read", Arguments: raw}})
		if err != nil {
			t.Fatal(err)
		}
		var outcome struct {
			OK     bool   `json:"ok"`
			Status string `json:"status"`
			Code   string `json:"code"`
		}
		if domain.Strict([]byte(result), &outcome) != nil || outcome.OK {
			t.Fatal(path, result)
		}
	}
	edit := workspace.NewTool("workspace_edit", w, authorize)
	raw := json.RawMessage(`{"path":"source.go","expected_sha256":"old","read_receipt_id":"x","old_text":"old","new_text":"new"}`)
	result, err := edit.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "edit", Name: "workspace_edit", Arguments: raw}})
	if err != nil || !strings.Contains(result, "approval_required") {
		t.Fatal(result, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "source.go"))
	if string(data) != "old\n" {
		t.Fatal("unauthorized write")
	}
}

func TestUnknownEditIsNotReappliedAfterFileReplacement(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("before\n"), 0600)
	binding, _ := workspace.Bind("w", root, []string{"a.go"}, []string{"a.go"}, true)
	directory := t.TempDir()
	fault := errors.New("lost result")
	w, err := workspace.New(binding, directory, func(stage workspace.Stage, r domain.Receipt) error {
		if stage == workspace.FileReplaced {
			return fault
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	authorize := func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 2}, nil
	}
	read := workspace.NewTool("workspace_read", w, authorize)
	raw := json.RawMessage(`{"path":"a.go","start_line":1,"line_count":10}`)
	result, err := read.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "read", Name: "workspace_read", Arguments: raw}})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	_ = domain.Strict([]byte(result), &out)
	var receipt string
	_ = json.Unmarshal(out["read_receipt_id"], &receipt)
	args, _ := domain.Marshal(map[string]any{"path": "a.go", "expected_sha256": domain.BytesHash([]byte("before\n")), "read_receipt_id": receipt, "old_text": "before", "new_text": "after"})
	request := agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "edit", Name: "workspace_edit", Arguments: args}}
	_, err = workspace.NewTool("workspace_edit", w, authorize).Execute(context.Background(), request)
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	_ = w.Close()
	w, err = workspace.New(binding, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, err = workspace.NewTool("workspace_edit", w, authorize).Execute(context.Background(), request)
	if !errors.Is(err, workspace.ErrUnknown) {
		t.Fatal("unknown replayed", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(data) != "after\n" {
		t.Fatal(string(data))
	}
}

func TestSearchOmitsSecretAndLinkedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("readable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, ".env"), filepath.Join(root, "alias.go")); err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w", root, []string{"."}, []string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workspace.New(binding, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tool := workspace.NewTool("workspace_search", w, func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 1}, nil
	})
	result, err := tool.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "search", Name: "workspace_search", Arguments: json.RawMessage(`{"mode":"files","path_prefix":"","query":"","limit":10}`)}})
	if err != nil || !strings.Contains(result, "a.go") || strings.Contains(result, "alias.go") || strings.Contains(result, ".env") || strings.Contains(result, "private") {
		t.Fatal(result, err)
	}
}

func TestEscapedOneMiBFileKeepsReadableDurableReceipts(t *testing.T) {
	root := t.TempDir()
	original := strings.Repeat("\n", (1<<20)-1) + "X"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	binding, err := workspace.Bind("w", root, []string{"a.go"}, []string{"a.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workspace.New(binding, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	authorize := func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 1}, nil
	}
	read := workspace.NewTool("workspace_read", w, authorize)
	raw, _ := domain.Marshal(map[string]any{"path": "a.go", "start_line": 1 << 20, "line_count": 1})
	result, err := read.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "read", Name: "workspace_read", Arguments: raw}})
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	_ = domain.Strict([]byte(result), &data)
	var receipt string
	_ = json.Unmarshal(data["read_receipt_id"], &receipt)
	args, _ := domain.Marshal(map[string]any{"path": "a.go", "expected_sha256": domain.BytesHash([]byte(original)), "read_receipt_id": receipt, "old_text": "X", "new_text": "Y"})
	result, err = workspace.NewTool("workspace_edit", w, authorize).Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "edit", Name: "workspace_edit", Arguments: args}})
	if err != nil || !strings.Contains(result, `"status":"applied"`) {
		t.Fatal(result, err)
	}
	receipts, err := w.Receipts(context.Background(), "agent")
	if err != nil || len(receipts) != 2 {
		t.Fatal("encoded receipt became unreadable", err, len(receipts))
	}
}

func TestOversizedReceiptIsReportedAsCorruptInsteadOfAnEmptyHistory(t *testing.T) {
	directory := t.TempDir()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("code\n"), 0600)
	binding, err := workspace.Bind("w", root, []string{"a.go"}, []string{}, false)
	if err != nil {
		t.Fatal(err)
	}
	w, err := workspace.New(binding, directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tool := workspace.NewTool("workspace_read", w, func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 1}, nil
	})
	if _, err = tool.Execute(context.Background(), agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "read", Name: "workspace_read", Arguments: json.RawMessage(`{"path":"a.go","start_line":1,"line_count":1}`)}}); err != nil {
		t.Fatal(err)
	}
	if err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "1.json" {
			return os.Truncate(path, int64(domain.MaxDocument)+1)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = workspace.LoadReceipts(context.Background(), directory, "agent"); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatal("oversize log hidden", err)
	}
}

func mustSpec(t *testing.T, tool agent.Tool) agent.ToolSpec {
	t.Helper()
	spec, err := tool.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
