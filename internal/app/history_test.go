package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
)

func TestClosedHistoricalToolEvidenceIsInputAndCannotBeReplayedOrReused(t *testing.T) {
	_, f := load(t)
	call := domain.Message{ID: "old-assistant-call", Role: "assistant", Content: "", Calls: []domain.Call{{ID: "old-call", Name: "add", Args: json.RawMessage(`{"a":1,"b":1}`), Type: "tool_call"}}}
	output := domain.Message{ID: "old-tool-output", Role: "tool", Content: "2", CallID: "old-call"}
	for _, entry := range []struct {
		m      domain.Message
		origin string
	}{{call, "provider"}, {output, "tool"}} {
		item, err := domain.NewItem(entry.m, entry.origin, "execution", []json.RawMessage{})
		if err != nil {
			t.Fatal(err)
		}
		f.Revision.Revision.History.Execution = append(f.Revision.Revision.History.Execution, item)
	}
	hash, err := f.Revision.ExpectedHash()
	if err != nil {
		t.Fatal(err)
	}
	f.Revision.Revision.ContentHash = hash
	data, err := domain.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	tool := &testTool{}
	a := open(t, app.Options{Tool: tool})
	ctx := context.Background()
	if err := a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(ctx, request(f, "history"))
	if err != nil || result.Answer != "42" || tool.calls.Load() != 1 {
		t.Fatal(result, err, tool.calls.Load())
	}
	current, err := a.Current(ctx, "w1", "c1")
	if err != nil || len(current.Revision.History.Execution) != 8 {
		t.Fatal(current, err)
	}
	badModel := modelFunc(func(context.Context, agent.ModelRequest, func(string) error) (agent.ModelResult, error) {
		return agent.ModelResult{Calls: []agent.ToolCall{{ID: "old-call", Name: "add", Arguments: json.RawMessage(`{}`)}}}, nil
	})
	otherTool := &testTool{}
	other := open(t, app.Options{Model: badModel, Tool: otherTool})
	if err := other.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	failed, err := other.Run(ctx, request(f, "collision"))
	if !errors.Is(err, domain.ErrExecutionIdentity) || failed.Run.Status != "error" || otherTool.calls.Load() != 0 {
		t.Fatal(failed, err, otherTool.calls.Load())
	}
	wrong, _ := domain.Copy(f)
	wrong.Revision.Revision.History.Execution[len(wrong.Revision.Revision.History.Execution)-1].Origin = "user_authored"
	if err := wrong.Validate(); !errors.Is(err, domain.ErrUnsupportedHistory) {
		t.Fatal("authored example promoted to execution evidence", err)
	}
}
