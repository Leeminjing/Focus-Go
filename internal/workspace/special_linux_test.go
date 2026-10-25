package workspace_test

import (
	"context"
	"encoding/json"
	"github.com/Leeminjing/Focus-Go/internal/workspace"
	"github.com/Leeminjing/go-forest-agent/agent"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNamedPipeIsRejectedBeforeOpeningEvenWhenNoWriterExists(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
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
	tool := workspace.NewTool("workspace_read", w, func(context.Context, agent.ToolRequest) (workspace.Authorization, error) {
		return workspace.Authorization{RunID: "focus", Workspace: binding, AgentRevision: 1}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	result, err := tool.Execute(ctx, agent.ToolRequest{RunID: "agent", Call: agent.ToolCall{ID: "read-pipe", Name: "workspace_read", Arguments: json.RawMessage(`{"path":"pipe","start_line":1,"line_count":1}`)}})
	if err != nil || !strings.Contains(result, `"ok":false`) || time.Since(start) >= time.Second {
		t.Fatal("special file blocked", result, err)
	}
}
