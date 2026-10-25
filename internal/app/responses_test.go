package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/trees/openairesponses"
)

func TestNativeResponsesEvidenceSurvivesCanonicalSealingAndPublicationRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Input []json.RawMessage `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid input")
		}
		results := []string{}
		for _, raw := range request.Input {
			var item struct {
				Type   string `json:"type"`
				Output string `json:"output"`
			}
			_ = json.Unmarshal(raw, &item)
			if item.Type == "function_call_output" {
				results = append(results, item.Output)
			}
		}
		var output []map[string]any
		call := func(id, name, args string) {
			output = []map[string]any{{"type": "reasoning", "id": "reason-" + id, "encrypted_content": "opaque-" + id, "summary": []any{}, "provider_score": 0.75}, {"type": "function_call", "id": "item-" + id, "call_id": id, "name": name, "arguments": args, "status": "completed"}}
		}
		switch len(results) {
		case 0:
			call("find", "workspace_search", `{ "mode":"files", "path_prefix":"", "query":"", "limit":10 }`)
		case 1:
			call("read", "workspace_read", `{ "path":"calc.go", "start_line":1, "line_count":20 }`)
		case 2:
			var read map[string]json.RawMessage
			_ = json.Unmarshal([]byte(results[1]), &read)
			var hash, receipt string
			_ = json.Unmarshal(read["sha256"], &hash)
			_ = json.Unmarshal(read["read_receipt_id"], &receipt)
			args, _ := json.Marshal(struct {
				Path     string `json:"path"`
				Expected string `json:"expected_sha256"`
				Receipt  string `json:"read_receipt_id"`
				Old      string `json:"old_text"`
				New      string `json:"new_text"`
			}{"calc.go", hash, receipt, "a-b", "a+b"})
			call("edit", "workspace_edit", string(args))
		case 3:
			call("verify", "workspace_read", `{"path":"calc.go","start_line":1,"line_count":20}`)
		default:
			if !strings.Contains(results[3], "a+b") {
				t.Error("postimage not reread")
			}
			output = []map[string]any{{"type": "message", "id": "answer", "status": "completed", "role": "assistant", "phase": "final_answer", "content": []map[string]any{{"type": "output_text", "text": "Fixed; tests not run."}}}}
		}
		data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp-%d", len(results)), "status": "completed", "output": output}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", data)
	}))
	defer server.Close()
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "calc.go"), []byte("package calc\nfunc Add(a,b int)int{return a-b}\n"), 0600)
	binding, err := workspace.Bind("w1", root, []string{"calc.go"}, []string{"calc.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	config := openairesponses.Config{Endpoint: server.URL + "/responses", Model: "fixture", APIKey: "not-a-real-key"}
	equipment, err := app.ResponsesEquipment(binding, config)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	fault := fmt.Errorf("publication interrupted")
	a := open(t, app.Options{Directory: directory, Coding: &equipment, Responses: &config, Observer: func(stage app.Stage, run domain.Run) error {
		if stage == app.ResultSealed {
			return fault
		}
		return nil
	}})
	data, f := load(t)
	if err = a.Import(context.Background(), data, nil); err != nil {
		t.Fatal(err)
	}
	first, err := a.Run(context.Background(), request(f, "native"))
	if err == nil || requests.Load() != 5 {
		t.Fatal(first, err, requests.Load())
	}
	// Removing neither source files nor result snapshots: only private execution
	// stores become unavailable. A sealed result must remain self-contained.
	if err = os.Rename(filepath.Join(directory, "agent"), filepath.Join(directory, "old-agent")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(directory, "responses"), filepath.Join(directory, "old-responses")); err != nil {
		t.Fatal(err)
	}
	second, err := app.OpenReader(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	finished, found, err := second.FinishSaved(context.Background(), first.Run.ID)
	if err != nil || !found || !finished.Verified || requests.Load() != 5 {
		t.Fatal(finished, err, requests.Load())
	}
	snapshot, err := second.Snapshot(context.Background(), *finished.Run.ResultRef)
	if err != nil || snapshot.Evidence == nil || len(snapshot.Evidence.ModelRecords) != 5 {
		t.Fatal(snapshot, err)
	}
}
