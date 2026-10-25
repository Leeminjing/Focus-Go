package app_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
)

func TestCodingRunUsesThreeTreesAndPublishesIndependentFileEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	original := "package calc\nfunc Add(a,b int)int{return a-b}\n"
	_ = os.WriteFile(filepath.Join(root, "calc.go"), []byte(original), 0600)
	module := "module codingfixture\n\ngo 1.25\n"
	testSource := "package calc\nimport \"testing\"\nfunc TestAdd(t *testing.T){if got:=Add(7,5);got!=12{t.Fatalf(\"got %d\",got)}}\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "calc_test.go"), []byte(testSource), 0600); err != nil {
		t.Fatal(err)
	}
	verifyProject := func() ([]byte, error) {
		cmd := exec.Command("go", "test", "./...", "-count=1")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		return cmd.CombinedOutput()
	}
	if output, err := verifyProject(); err == nil {
		t.Fatal("fixture did not contain a failing function", string(output))
	}
	binding, err := workspace.Bind("w1", root, []string{"calc.go"}, []string{"calc.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := app.CodingEquipment(binding, "offline-coding-v1")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	model := modelFunc(func(ctx context.Context, request agent.ModelRequest, emit func(string) error) (agent.ModelResult, error) {
		calls.Add(1)
		results := []string{}
		for _, m := range request.Messages {
			if m.Role == "tool" {
				results = append(results, m.Content)
			}
		}
		call := func(id, name string, args any) (agent.ModelResult, error) {
			raw, err := domain.Marshal(args)
			return agent.ModelResult{Calls: []agent.ToolCall{{ID: id, Name: name, Arguments: raw}}}, err
		}
		switch len(results) {
		case 0:
			return call("find", "workspace_search", map[string]any{"mode": "files", "path_prefix": "", "query": "", "limit": 10})
		case 1:
			if !strings.Contains(results[0], "calc.go") {
				t.Error("search not used")
			}
			return call("read", "workspace_read", map[string]any{"path": "calc.go", "start_line": 1, "line_count": 200})
		case 2:
			var out struct {
				OK        bool   `json:"ok"`
				Status    string `json:"status"`
				Path      string `json:"path"`
				Hash      string `json:"sha256"`
				Bytes     int    `json:"bytes"`
				Start     int    `json:"start_line"`
				End       int    `json:"end_line"`
				Content   string `json:"content"`
				Truncated bool   `json:"truncated"`
				Receipt   string `json:"read_receipt_id"`
			}
			if err := domain.Strict([]byte(results[1]), &out); err != nil {
				return agent.ModelResult{}, err
			}
			if !strings.Contains(out.Content, "a-b") {
				t.Error("actual source missing")
			}
			return call("edit", "workspace_edit", map[string]any{"path": out.Path, "expected_sha256": out.Hash, "read_receipt_id": out.Receipt, "old_text": "a-b", "new_text": "a+b"})
		case 3:
			if !strings.Contains(results[2], `"status":"applied"`) {
				t.Error(results[2])
			}
			return call("verify", "workspace_read", map[string]any{"path": "calc.go", "start_line": 1, "line_count": 200})
		default:
			if !strings.Contains(results[3], "a+b") {
				t.Error("actual postimage not reread")
			}
			return agent.ModelResult{Content: "Fixed Add; tests not run by the agent."}, nil
		}
	})
	a := open(t, app.Options{Model: model, ModelProfile: equipment.ModelProfile, Coding: &equipment})
	data, f := load(t)
	if err := a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	request := request(f, "coding")
	request.Input = "Fix Add without changing other code."
	result, err := a.Run(ctx, request)
	if err != nil || !result.Verified || result.Run.Version != domain.CodingVersion {
		t.Fatal(result, err)
	}
	if result.Run.ResultRef == nil {
		t.Fatal(result)
	}
	snapshot, err := a.Snapshot(ctx, *result.Run.ResultRef)
	if err != nil || snapshot.Evidence == nil || len(snapshot.Evidence.Receipts) != 4 || snapshot.Validate() != nil {
		t.Fatal(snapshot, err)
	}
	actual, _ := os.ReadFile(filepath.Join(root, "calc.go"))
	if string(actual) != "package calc\nfunc Add(a,b int)int{return a+b}\n" {
		t.Fatal(string(actual))
	}
	if output, err := verifyProject(); err != nil {
		t.Fatal("independent tests failed", err, string(output))
	}
	for name, want := range map[string]string{"go.mod": module, "calc_test.go": testSource} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatal("unrelated file changed", name, err)
		}
	}
	before := calls.Load()
	again, err := a.Run(ctx, request)
	if err != nil || again.Run.ID != result.Run.ID || calls.Load() != before {
		t.Fatal(again, err)
	}
	for _, r := range snapshot.Evidence.Receipts {
		var object map[string]json.RawMessage
		if domain.Strict([]byte(r.Result), &object) != nil {
			t.Fatal(r.Result)
		}
	}
}
