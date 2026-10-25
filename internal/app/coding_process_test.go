package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
)

func codingProcessModel(ctx context.Context, request agent.ModelRequest, emit func(string) error) (agent.ModelResult, error) {
	results := []string{}
	for _, m := range request.Messages {
		if m.Role == "tool" {
			results = append(results, m.Content)
		}
	}
	if len(results) >= 2 {
		return agent.ModelResult{Content: "Fixed; tests not run."}, nil
	}
	name, id := "workspace_read", "read"
	args := map[string]any{"path": "calc.go", "start_line": 1, "line_count": 20}
	if len(results) == 1 {
		var read map[string]json.RawMessage
		if err := domain.Strict([]byte(results[0]), &read); err != nil {
			return agent.ModelResult{}, err
		}
		var hash, receipt string
		_ = json.Unmarshal(read["sha256"], &hash)
		_ = json.Unmarshal(read["read_receipt_id"], &receipt)
		name, id = "workspace_edit", "edit"
		args = map[string]any{"path": "calc.go", "expected_sha256": hash, "read_receipt_id": receipt, "old_text": "a-b", "new_text": "a+b"}
	}
	raw, err := domain.Marshal(args)
	return agent.ModelResult{Calls: []agent.ToolCall{{ID: id, Name: name, Arguments: raw}}}, err
}

func TestCodingProcessFixture(t *testing.T) {
	mode := os.Getenv("FOCUS_CODING_PROCESS_MODE")
	if mode == "" {
		t.Skip("subprocess fixture")
	}
	directory := os.Getenv("FOCUS_CODING_DIRECTORY")
	stage := os.Getenv("FOCUS_CODING_STAGE")
	root := filepath.Join(directory, "project")
	state := filepath.Join(directory, "state")
	binding, err := workspace.Bind("w1", root, []string{"calc.go"}, []string{"calc.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	equipment, err := app.CodingEquipment(binding, "process-coding-v1")
	if err != nil {
		t.Fatal(err)
	}
	signal := func(runID string) {
		data, _ := json.Marshal(map[string]string{"run_id": runID})
		fmt.Println(string(data))
		<-time.After(30 * time.Second)
	}
	a := open(t, app.Options{Directory: state, Coding: &equipment, ModelProfile: equipment.ModelProfile, Model: modelFunc(codingProcessModel), WorkspaceObserver: func(observed workspace.Stage, r domain.Receipt) error {
		if observed == workspace.FileReplaced {
			f, err := os.OpenFile(filepath.Join(directory, "effects.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			if _, err = f.WriteString("effect\n"); err != nil {
				f.Close()
				return err
			}
			if err = f.Sync(); err != nil {
				f.Close()
				return err
			}
			if err = f.Close(); err != nil {
				return err
			}
		}
		if mode == "execute" && r.Tool == "workspace_edit" && string(observed) == stage {
			signal(r.RunID)
		}
		return nil
	}, Observer: func(observed app.Stage, r domain.Run) error {
		if mode == "execute" && observed == app.AgentCompleted && stage == string(app.AgentCompleted) {
			signal(r.ID)
		}
		return nil
	}})
	ctx := context.Background()
	if mode == "execute" {
		data, f := load(t)
		if err = a.Import(ctx, data, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = a.Run(ctx, request(f, "process-coding")); err != nil {
			t.Fatal(err)
		}
		return
	}
	id := os.Getenv("FOCUS_CODING_RUN")
	record, err := a.AgentState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status == agent.ToolRunning {
		record, err = a.Recover(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status != agent.OutcomeUnknown {
			t.Fatal(record)
		}
		if stage == string(workspace.Prepared) {
			data, _ := os.ReadFile(filepath.Join(root, "calc.go"))
			if !bytes.Contains(data, []byte("a-b")) {
				t.Fatal("prepared action unexpectedly wrote")
			}
			if _, err = a.Resume(ctx, id); err == nil {
				t.Fatal("unknown blindly resumed")
			}
			fmt.Println("unknown-verified")
			return
		}
		operator := ""
		if stage == string(workspace.FileReplaced) {
			data, _ := os.ReadFile(filepath.Join(root, "calc.go"))
			if !bytes.Contains(data, []byte("a+b")) {
				t.Fatal("postimage not independently confirmed")
			}
			operator = "integration-operator"
		}
		record, err = a.Reconcile(ctx, id, record.Revision, "edit", operator)
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := a.Resume(ctx, id)
	if err != nil || !result.Verified {
		t.Fatal(result, err)
	}
	catalog, err := a.State(ctx)
	if err != nil || len(catalog.Revisions) != 2 || len(catalog.Events) != 1 {
		t.Fatal(catalog, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "effects.log"))
	if err != nil || string(data) != "effect\n" {
		t.Fatal("duplicate effect", string(data), err)
	}
	fmt.Println("recovery-verified")
}

func TestRealCodingProcessesNeverReplayUnknownOrCompletedFileWrites(t *testing.T) {
	for _, stage := range []string{string(workspace.Prepared), string(workspace.FileReplaced), string(workspace.ReceiptSaved), string(app.AgentCompleted)} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Mkdir(filepath.Join(directory, "project"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "project", "calc.go"), []byte("package calc\nfunc Add(a,b int)int{return a-b}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestCodingProcessFixture$", "-test.timeout=35s")
			cmd.Env = append(os.Environ(), "FOCUS_CODING_PROCESS_MODE=execute", "FOCUS_CODING_DIRECTORY="+directory, "FOCUS_CODING_STAGE="+stage)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				if scanner.Scan() {
					ready <- scanner.Text()
				} else {
					ready <- ""
				}
			}()
			var line string
			select {
			case line = <-ready:
			case <-time.After(8 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("no coding boundary", stderr.String())
			}
			var signal struct {
				ID string `json:"run_id"`
			}
			if json.Unmarshal([]byte(line), &signal) != nil || signal.ID == "" {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(line, stderr.String())
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if strings.Contains(stderr.String(), "WARNING: DATA RACE") {
				t.Fatal(stderr.String())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			recovery := exec.CommandContext(ctx, binary, "-test.run=^TestCodingProcessFixture$", "-test.timeout=11s")
			recovery.Env = append(os.Environ(), "FOCUS_CODING_PROCESS_MODE=recover", "FOCUS_CODING_DIRECTORY="+directory, "FOCUS_CODING_STAGE="+stage, "FOCUS_CODING_RUN="+signal.ID)
			output, err := recovery.CombinedOutput()
			expected := "recovery-verified"
			if stage == string(workspace.Prepared) {
				expected = "unknown-verified"
			}
			if err != nil || !strings.Contains(string(output), expected) {
				t.Fatal(err, string(output))
			}
		})
	}
}
