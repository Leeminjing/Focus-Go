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
	"github.com/Leeminjing/go-forest-agent/agent"
)

func TestFocusProcessFixture(t *testing.T) {
	mode := os.Getenv("FOCUS_GO_PROCESS_MODE")
	if mode == "" {
		t.Skip("subprocess fixture")
	}
	directory := os.Getenv("FOCUS_GO_PROCESS_DIRECTORY")
	tool := &testTool{execute: func(ctx context.Context, request agent.ToolRequest) (string, error) {
		file, err := os.OpenFile(filepath.Join(directory, "effects.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		if _, err := file.WriteString("effect\n"); err != nil {
			file.Close()
			return "", err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		return "42", nil
	}}
	observer := func(stage app.Stage, run domain.Run) error {
		if string(stage) == mode {
			encoded, _ := json.Marshal(struct {
				RunID string    `json:"run_id"`
				Stage app.Stage `json:"stage"`
			}{run.ID, stage})
			fmt.Println(string(encoded))
			<-time.After(30 * time.Second)
		}
		return nil
	}
	a := open(t, app.Options{Directory: directory, Tool: tool, Observer: observer})
	ctx := context.Background()
	if mode == "recover" {
		result, err := a.Resume(ctx, os.Getenv("FOCUS_GO_PROCESS_RUN"))
		if err != nil || result.Answer != "42" || !result.Verified {
			t.Fatal(result, err)
		}
		state, err := a.State(ctx)
		if err != nil || len(state.Revisions) != 2 || len(state.Runs) != 1 || len(state.Events) != 1 {
			t.Fatal(state, err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "effects.log"))
		if err != nil || strings.Count(string(data), "effect\n") != 1 {
			t.Fatal("tool replayed", err, string(data))
		}
		fmt.Println("recovery-verified")
		return
	}
	data, f := load(t)
	if err := a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, request(f, "process")); err != nil {
		t.Fatal(err)
	}
}

func TestRealProcessPublicationBoundariesRecoverWithoutDuplicateEffects(t *testing.T) {
	for _, stage := range []app.Stage{app.Admitted, app.AgentCompleted, app.ResultSealed, app.SettlementCommitted} {
		t.Run(string(stage), func(t *testing.T) {
			directory := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestFocusProcessFixture$", "-test.timeout=35s")
			cmd.Env = append(os.Environ(), "FOCUS_GO_PROCESS_MODE="+string(stage), "FOCUS_GO_PROCESS_DIRECTORY="+directory)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
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
			case <-time.After(7 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("no process boundary", stderr.String())
			}
			var signal struct {
				RunID string    `json:"run_id"`
				Stage app.Stage `json:"stage"`
			}
			if err := json.Unmarshal([]byte(line), &signal); err != nil || signal.RunID == "" || signal.Stage != stage {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal(line, err, stderr.String())
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if strings.Contains(stderr.String(), "WARNING: DATA RACE") {
				t.Fatal(stderr.String())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			recovery := exec.CommandContext(ctx, executable, "-test.run=^TestFocusProcessFixture$", "-test.timeout=9s")
			recovery.Env = append(os.Environ(), "FOCUS_GO_PROCESS_MODE=recover", "FOCUS_GO_PROCESS_DIRECTORY="+directory, "FOCUS_GO_PROCESS_RUN="+signal.RunID)
			output, err := recovery.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "recovery-verified") {
				t.Fatal(err, string(output))
			}
		})
	}
}
