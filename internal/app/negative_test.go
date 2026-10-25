package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func TestBadContractsAndAnyReferenceMismatchFailBeforeTools(t *testing.T) {
	data, f := load(t)
	ctx := context.Background()
	tool := &testTool{}
	a := open(t, app.Options{Tool: tool})
	mutations := []func(*domain.Fixture){
		func(f *domain.Fixture) { f.Revision.Backend.Engine = "langgraph" },
		func(f *domain.Fixture) { f.Revision.Revision.ContentHash = "invalid" },
		func(f *domain.Fixture) { f.Revision.HashProfile = "unknown" },
		func(f *domain.Fixture) { f.Revision.Revision.History.SchemaVersion = 1 },
		func(f *domain.Fixture) { f.Revision.Revision.History.Execution[0].Kind = "reasoning" },
		func(f *domain.Fixture) {
			f.Revision.Revision.History.Execution[0].Payload = json.RawMessage(`{"message":{"id":"seed-user","role":"user","content":[{"type":"image"}]}}`)
		},
		func(f *domain.Fixture) {
			f.Revision.Revision.History.Execution[1].ItemID = f.Revision.Revision.History.Execution[0].ItemID
		},
		func(f *domain.Fixture) {
			f.Revision.Revision.History.Execution[1].Payload = json.RawMessage(`{"message":{"id":"seed-assistant","role":"assistant","content":"","tool_calls":[{"id":"unclosed","name":"add","args":{}}]}}`)
		},
		func(f *domain.Fixture) {
			f.Revision.Revision.History.Execution[1].Payload = json.RawMessage(`{"message":{"id":"seed-assistant","role":"assistant","content":"","tool_calls":[{"id":"dup","name":"add","args":{}},{"id":"dup","name":"add","args":{}}]}}`)
		},
	}
	for i, mutate := range mutations {
		bad, _ := domain.Copy(f)
		mutate(&bad)
		if hash, err := bad.Revision.ExpectedHash(); err == nil && i != 1 {
			bad.Revision.Revision.ContentHash = hash
		}
		encoded, err := domain.Marshal(bad)
		if err == nil {
			err = a.Import(ctx, encoded, nil)
		}
		if err == nil {
			t.Fatal("invalid contract accepted", i)
		}
	}
	state, err := a.State(ctx)
	if err != nil || len(state.Contexts) != 0 || len(state.Runs) != 0 || tool.calls.Load() != 0 {
		t.Fatal(state, err)
	}
	if err := a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Ref){func(r *domain.Ref) { r.ContextID = "other" }, func(r *domain.Ref) { r.RevisionID = "other" }, func(r *domain.Ref) { r.Generation++ }, func(r *domain.Ref) { r.ExecutionThreadID = "other" }, func(r *domain.Ref) { r.CheckpointNS = "other" }, func(r *domain.Ref) { r.CheckpointID = domain.Pointer("other") }, func(r *domain.Ref) { r.PayloadMode = "definition" }} {
		ref, _ := domain.Copy(f.Context.Current)
		mutate(&ref)
		if _, err := a.Revision(ctx, ref); err == nil {
			t.Fatal("reference mismatch accepted", ref)
		}
	}
	if tool.calls.Load() != 0 {
		t.Fatal("invalid contracts caused execution")
	}
}

func TestPublicationFailureAfterToolCompletionResumesWithoutReexecution(t *testing.T) {
	data, f := load(t)
	tool := &testTool{}
	directory := t.TempDir()
	fault := errors.New("simulated interrupted publication")
	a := open(t, app.Options{Directory: directory, Tool: tool, Observer: func(stage app.Stage, _ domain.Run) error {
		if stage == app.ResultSealed {
			return fault
		}
		return nil
	}})
	if err := a.Import(context.Background(), data, nil); err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(context.Background(), request(f, "retry"))
	if !errors.Is(err, fault) || tool.calls.Load() != 1 {
		t.Fatal(result, err)
	}
	before, err := a.State(context.Background())
	if err != nil || len(before.Revisions) != 1 || len(before.Events) != 0 {
		t.Fatal(before, err)
	}
	second := open(t, app.Options{Directory: directory, Tool: tool})
	finished, err := second.Resume(context.Background(), result.Run.ID)
	if err != nil || !finished.Verified || finished.Answer != "42" || tool.calls.Load() != 1 {
		t.Fatal(finished, err, tool.calls.Load())
	}
}

func TestMissingAgentStorageNeverReplaysCompletedTools(t *testing.T) {
	for _, stage := range []app.Stage{app.ResultSealed, app.AgentCompleted} {
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			data, f := load(t)
			tool := &testTool{}
			directory := t.TempDir()
			fault := errors.New("publication interrupted")
			a := open(t, app.Options{Directory: directory, Tool: tool, Observer: func(observed app.Stage, _ domain.Run) error {
				if observed == stage {
					return fault
				}
				return nil
			}})
			if err := a.Import(ctx, data, nil); err != nil {
				t.Fatal(err)
			}
			first, err := a.Run(ctx, request(f, "missing-agent"))
			if !errors.Is(err, fault) || tool.calls.Load() != 1 {
				t.Fatal(first, err, tool.calls.Load())
			}
			if err := os.Rename(filepath.Join(directory, "agent"), filepath.Join(directory, "agent-unavailable")); err != nil {
				t.Fatal(err)
			}
			second := open(t, app.Options{Directory: directory, Tool: tool})
			finished, err := second.Resume(ctx, first.Run.ID)
			if stage == app.ResultSealed {
				if err != nil || !finished.Verified || finished.Answer != "42" {
					t.Fatal("sealed result must settle independently", finished, err)
				}
			} else if !errors.Is(err, domain.ErrExecutionIdentity) {
				t.Fatal("missing existing checkpoint must fail explicitly", finished, err)
			}
			if tool.calls.Load() != 1 {
				t.Fatal("completed effect replayed", tool.calls.Load())
			}
			state, err := second.State(ctx)
			if err != nil || len(state.Runs) != 1 {
				t.Fatal(state, err)
			}
			if stage == app.ResultSealed {
				if len(state.Revisions) != 2 || len(state.Events) != 1 {
					t.Fatal(state)
				}
			} else if len(state.Revisions) != 1 || len(state.Events) != 0 {
				t.Fatal("unsealed result published", state)
			}
		})
	}
}
