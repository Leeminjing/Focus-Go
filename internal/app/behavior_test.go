package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
)

type testTool struct {
	calls    atomic.Int32
	approval bool
	execute  func(context.Context, agent.ToolRequest) (string, error)
}

func (t *testTool) Describe(context.Context) (agent.ToolSpec, error) {
	return agent.ToolSpec{Name: "add", Version: "1", Parameters: json.RawMessage(`{"type":"object"}`), NeedsApproval: t.approval}, nil
}
func (t *testTool) Execute(ctx context.Context, request agent.ToolRequest) (string, error) {
	t.calls.Add(1)
	if t.execute != nil {
		return t.execute(ctx, request)
	}
	var args struct {
		A int64 `json:"a"`
		B int64 `json:"b"`
	}
	if err := domain.Strict(request.Call.Arguments, &args); err != nil {
		return "", err
	}
	return fmt.Sprint(args.A + args.B), nil
}

type modelFunc func(context.Context, agent.ModelRequest, func(string) error) (agent.ModelResult, error)

func (f modelFunc) Generate(ctx context.Context, r agent.ModelRequest, emit func(string) error) (agent.ModelResult, error) {
	return f(ctx, r, emit)
}
func load(t *testing.T) ([]byte, domain.Fixture) {
	t.Helper()
	data, err := os.ReadFile("../../testdata/base-40.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture domain.Fixture
	if err := domain.Strict(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return data, fixture
}
func open(t *testing.T, options app.Options) *app.Application {
	t.Helper()
	if options.Directory == "" {
		options.Directory = t.TempDir()
	}
	a, err := app.New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return a
}
func request(f domain.Fixture, key string) domain.Request {
	return domain.Request{WorkspaceID: f.Context.WorkspaceID, ContextID: f.Context.ID, Expected: f.Context.Current, Input: "ADD=2", Key: key}
}

func TestFrozenHistorySurvivesCurrentChangeWithoutOverwritingNewRevision(t *testing.T) {
	data, fixture := load(t)
	tool := &testTool{}
	var a *app.Application
	var changed atomic.Bool
	a = open(t, app.Options{Tool: tool, Observer: func(stage app.Stage, run domain.Run) error {
		if stage != app.Admitted || !changed.CompareAndSwap(false, true) {
			return nil
		}
		next, _ := domain.Copy(fixture)
		ref := next.Revision.Revision.Ref
		ref.RevisionID = "external-r2"
		ref.Generation = 2
		ref.CheckpointID = domain.Pointer("external-cp")
		next.Context.Current = ref
		next.Revision.Revision.Ref = ref
		for i := range next.Revision.Revision.History.Execution {
			m, err := next.Revision.Revision.History.Execution[i].Message()
			if err != nil {
				return err
			}
			if m.Role == "user" {
				m.Content = "BASE=99"
				item, err := domain.NewItem(m, "direct_user", "revision", []json.RawMessage{})
				if err != nil {
					return err
				}
				next.Revision.Revision.History.Execution[i] = item
				next.Revision.Revision.History.Authored[0] = item
			}
		}
		hash, err := next.Revision.ExpectedHash()
		if err != nil {
			return err
		}
		next.Revision.Revision.ContentHash = hash
		encoded, err := domain.Marshal(next)
		if err != nil {
			return err
		}
		return a.Import(context.Background(), encoded, &fixture.Context.Current)
	}})
	if err := a.Import(context.Background(), data, nil); err != nil {
		t.Fatal(err)
	}
	outcome, err := a.Run(context.Background(), request(fixture, "superseded"))
	if err != nil || outcome.Answer != "42" || outcome.Run.Status != "success" || outcome.Run.Settlement == nil || outcome.Run.Settlement.Publication != "superseded" || outcome.Run.Settlement.ContextRevision != nil || outcome.Current.RevisionID != "external-r2" || tool.calls.Load() != 1 {
		t.Fatal(outcome, err, tool.calls.Load())
	}
	state, err := a.State(context.Background())
	if err != nil || len(state.Revisions) != 2 {
		t.Fatal(state, err)
	}
}

func TestApprovalAndUnknownRemainUnsettledAndNeverPublishSuccess(t *testing.T) {
	for _, mode := range []string{"approval", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			tool := &testTool{approval: mode == "approval"}
			if mode == "unknown" {
				tool.execute = func(context.Context, agent.ToolRequest) (string, error) {
					return "", errors.New("effect might have happened")
				}
			}
			a := open(t, app.Options{Tool: tool})
			data, f := load(t)
			ctx := context.Background()
			if err := a.Import(ctx, data, nil); err != nil {
				t.Fatal(err)
			}
			outcome, err := a.Run(ctx, request(f, "paused"))
			if mode == "unknown" && !errors.Is(err, agent.ErrUnknown) {
				t.Fatal(err)
			}
			if mode == "approval" && err != nil {
				t.Fatal(err)
			}
			if outcome.Run.Status != "interrupted" || outcome.Run.Reason == "" || outcome.Run.Settlement != nil || !outcome.Current.Equal(f.Context.Current) {
				t.Fatal(outcome)
			}
			if _, err := a.Run(ctx, request(f, "other")); !errors.Is(err, domain.ErrBusy) {
				t.Fatal(err)
			}
			record, err := a.AgentState(ctx, outcome.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			call := record.Calls[record.Current].Call.ID
			if mode == "approval" {
				if tool.calls.Load() != 0 {
					t.Fatal("executed before approval")
				}
				if _, err := a.Decide(ctx, outcome.Run.ID, record.Revision, call, agent.Granted); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := a.Resume(ctx, outcome.Run.ID); !errors.Is(err, agent.ErrUnknown) || tool.calls.Load() != 1 {
					t.Fatal("unknown replay", err, tool.calls.Load())
				}
				if _, err := a.Resolve(ctx, outcome.Run.ID, call, "42"); err != nil {
					t.Fatal(err)
				}
			}
			finished, err := a.Resume(ctx, outcome.Run.ID)
			if err != nil || finished.Run.Status != "success" || finished.Answer != "42" || tool.calls.Load() != 1 {
				t.Fatal(finished, err, tool.calls.Load())
			}
		})
	}
}

func TestFailureCancellationAndTimeoutDoNotAdvanceContext(t *testing.T) {
	for _, mode := range []string{"failure", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{})
			model := modelFunc(func(ctx context.Context, _ agent.ModelRequest, _ func(string) error) (agent.ModelResult, error) {
				close(entered)
				if mode == "failure" {
					return agent.ModelResult{}, errors.New("model failed")
				}
				<-ctx.Done()
				return agent.ModelResult{}, ctx.Err()
			})
			a := open(t, app.Options{Model: model})
			data, f := load(t)
			if err := a.Import(context.Background(), data, nil); err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			var outcome app.Outcome
			done := make(chan error, 1)
			go func() { var err error; outcome, err = a.Run(ctx, request(f, mode)); done <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model did not start")
			}
			if mode == "cancel" {
				cancel()
			}
			err := <-done
			if err == nil {
				t.Fatal("missing execution error")
			}
			want := "error"
			if mode == "cancel" {
				want = "interrupted"
			}
			if mode == "timeout" {
				want = "timeout"
			}
			if outcome.Run.Status != want || outcome.Run.Settlement == nil || outcome.Run.Settlement.Publication != "not_applicable" || outcome.Run.Reason == "" || !outcome.Current.Equal(f.Context.Current) {
				t.Fatal(outcome, err)
			}
			state, err := a.State(context.Background())
			if err != nil || len(state.Revisions) != 1 || len(state.Events) != 1 {
				t.Fatal(state, err)
			}
		})
	}
}

func TestConcurrentContextsAreIsolatedAndDuplicateAdmissionIsSafe(t *testing.T) {
	a := open(t, app.Options{})
	data, f := load(t)
	ctx := context.Background()
	if err := a.Import(ctx, data, nil); err != nil {
		t.Fatal(err)
	}
	other, _ := domain.Copy(f)
	other.Context.ID = "c2"
	ref := other.Context.Current
	ref.ContextID = "c2"
	ref.RevisionID = "c2-r1"
	ref.ExecutionThreadID = "thread-c2"
	ref.CheckpointID = domain.Pointer("c2-seed")
	other.Context.Current = ref
	other.Revision.Revision.Ref = ref
	hash, _ := other.Revision.ExpectedHash()
	other.Revision.Revision.ContentHash = hash
	encoded, _ := domain.Marshal(other)
	if err := a.Import(ctx, encoded, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, f := range []domain.Fixture{f, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := a.Run(ctx, request(f, "key-"+f.Context.ID))
			if err == nil && (out.Answer != "42" || out.Current.ContextID != f.Context.ID) {
				err = domain.ErrExecutionIdentity
			}
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := a.State(ctx)
	if err != nil || len(state.Revisions) != 4 || len(state.Events) != 2 {
		t.Fatal(state, err)
	}
}
