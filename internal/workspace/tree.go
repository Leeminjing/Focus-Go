package workspace

import (
	"context"
	"sort"
	"strings"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
	"github.com/Leeminjing/go-forest/forest"
)

type BindingReader interface {
	Binding(context.Context, string) (domain.Run, domain.Envelope, error)
}
type ExecutionReader interface {
	Read(context.Context, string) (agent.Run, error)
}
type toolAccess interface {
	Execute(context.Context, string, Authorization, agent.ToolRequest) (string, error)
}
type EvidenceAccess interface {
	Preview(context.Context, domain.Run, agent.Run) (map[string]any, error)
	Receipts(context.Context, string) ([]domain.Receipt, error)
	Reconcile(context.Context, domain.Run, agent.Run, string) (string, error)
	Drift(context.Context, []domain.Receipt) ([]string, error)
}

func (w *Workspace) Preview(ctx context.Context, run domain.Run, record agent.Run) (map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if run.Equipment == nil || record.ID != run.AgentRunID || record.Status != agent.AwaitingApproval || record.Current < 0 || record.Current >= len(record.Calls) {
		return nil, domain.ErrExecutionIdentity
	}
	grant, _ := domain.Hash(w.binding)
	expected, _ := domain.Hash(run.Equipment.Workspace)
	if grant != expected {
		return nil, domain.ErrExecutionIdentity
	}
	call := record.Calls[record.Current].Call
	if call.Name != "workspace_edit" {
		return nil, domain.ErrInvalid
	}
	args, err := (&tool{kind: call.Name, resource: w}).parse(call.Arguments)
	if err != nil {
		return nil, err
	}
	if !allowed(w.binding.Write, args.Path) {
		return nil, ErrDenied
	}
	data, _, err := w.read(args.Path)
	if err != nil {
		return nil, err
	}
	if domain.BytesHash(data) != args.Expected {
		return nil, ErrVersion
	}
	r, _, err := w.load(ctx, record.ID, args.ReadReceipt)
	if err != nil || r.Tool != "workspace_read" || r.RunID != run.ID || r.Path != args.Path || r.BeforeHash != args.Expected {
		return nil, domain.ErrExecutionIdentity
	}
	return map[string]any{"path": args.Path, "expected_sha256": args.Expected, "read_receipt_id": args.ReadReceipt, "old_text": args.Old, "new_text": args.New, "call_id": call.ID, "agent_revision": record.Revision}, nil
}

func (w *Workspace) Execute(ctx context.Context, kind string, auth Authorization, request agent.ToolRequest) (string, error) {
	return NewTool(kind, w, func(context.Context, agent.ToolRequest) (Authorization, error) { return auth, nil }).Execute(ctx, request)
}
func RootDefinition(binding domain.Workspace, directory string, observer func(Stage, domain.Receipt) error) forest.Definition {
	return forest.Definition{TypeID: "focus.workspace", Version: "1", ExportChildren: true, Capabilities: []forest.CapabilityDescriptor{forest.Capability[toolAccess]("files", forest.Descendants), forest.Capability[EvidenceAccess]("evidence", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		w, err := New(binding, directory, observer)
		if err != nil {
			return nil, err
		}
		if err = forest.Provide(env, "files", w); err != nil {
			w.Close()
			return nil, err
		}
		if err = forest.Provide(env, "evidence", w); err != nil {
			w.Close()
			return nil, err
		}
		return forest.Hooks{OnStop: func(context.Context) error { return w.Close() }}, nil
	}}
}

type linkedTool struct {
	kind        string
	preapproved bool
	files       forest.Ref[toolAccess]
	binding     forest.Ref[BindingReader]
	execution   forest.Ref[ExecutionReader]
}

func (t *linkedTool) Describe(ctx context.Context) (agent.ToolSpec, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolSpec{}, err
	}
	return Schema(t.kind, t.preapproved)
}
func (t *linkedTool) Execute(ctx context.Context, request agent.ToolRequest) (string, error) {
	var run domain.Run
	var record agent.Run
	if err := t.binding.Use(ctx, func(b BindingReader) error { var err error; run, _, err = b.Binding(ctx, request.RunID); return err }); err != nil {
		return "", err
	}
	if run.Equipment == nil || run.Settlement != nil {
		return "", domain.ErrExecutionIdentity
	}
	if err := t.execution.Use(ctx, func(e ExecutionReader) error { var err error; record, err = e.Read(ctx, request.RunID); return err }); err != nil {
		return "", err
	}
	if record.Status != agent.ToolRunning || record.Current < 0 || record.Current >= len(record.Calls) {
		return "", domain.ErrExecutionIdentity
	}
	pending := record.Calls[record.Current]
	actualHash, err := domain.Hash(request.Call)
	if err != nil {
		return "", err
	}
	pendingHash, _ := domain.Hash(pending.Call)
	spec, _ := Schema(t.kind, t.preapproved)
	specHash, _ := domain.Hash(spec)
	pendingSpecHash, _ := domain.Hash(pending.Tool)
	if pendingHash != actualHash || specHash != pendingSpecHash || (spec.NeedsApproval && !pending.Approved) {
		return "", domain.ErrExecutionIdentity
	}
	var result string
	err = t.files.Use(ctx, func(files toolAccess) error {
		var err error
		result, err = files.Execute(ctx, t.kind, Authorization{RunID: run.ID, Workspace: run.Equipment.Workspace, Approved: pending.Approved, AgentRevision: record.Revision}, request)
		return err
	})
	return result, err
}
func ToolDefinition(kind string, preapproved bool) forest.Definition {
	return forest.Definition{TypeID: "focus." + kind, Version: "1", Ports: []forest.PortDescriptor{forest.Port[toolAccess]("files", forest.Required(), forest.During(forest.Construction)), forest.Port[BindingReader]("binding", forest.Required()), forest.Port[ExecutionReader]("execution", forest.Required())}, Capabilities: []forest.CapabilityDescriptor{forest.Capability[agent.Tool]("tool", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		files, err := forest.Reference[toolAccess](env, "files")
		if err != nil {
			return nil, err
		}
		binding, err := forest.Reference[BindingReader](env, "binding")
		if err != nil {
			return nil, err
		}
		execution, err := forest.Reference[ExecutionReader](env, "execution")
		if err != nil {
			return nil, err
		}
		if _, err = Schema(kind, preapproved); err != nil {
			return nil, err
		}
		return forest.Hooks{}, forest.Provide(env, "tool", &linkedTool{kind, preapproved, files, binding, execution})
	}}
}
func (w *Workspace) Reconcile(ctx context.Context, run domain.Run, record agent.Run, operator string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if run.Equipment == nil || record.ID != run.AgentRunID || record.Status != agent.OutcomeUnknown || record.Current < 0 || record.Current >= len(record.Calls) {
		return "", domain.ErrExecutionIdentity
	}
	grant, _ := domain.Hash(w.binding)
	expected, _ := domain.Hash(run.Equipment.Workspace)
	if grant != expected {
		return "", domain.ErrExecutionIdentity
	}
	call := record.Calls[record.Current].Call
	key, _ := domain.Hash(struct{ Run, Agent, Call string }{run.ID, record.ID, call.ID})
	id := domain.StableID("receipt", key, 0)
	r, seq, err := w.load(ctx, record.ID, id)
	if err != nil {
		return "", err
	}
	hash, err := domain.HashJSON(call.Arguments)
	if err != nil || r.ArgumentsHash != hash || r.Tool != call.Name || r.RunID != run.ID || r.GrantHash != grant || r.ToolVersion != record.Calls[record.Current].Tool.Version {
		return "", domain.ErrExecutionIdentity
	}
	if r.Phase != "prepared" {
		return r.Result, nil
	}
	if !domain.ID(operator) || strings.ContainsAny(operator, "\r\n") {
		return "", ErrUnknown
	}
	data, _, err := w.read(r.Path)
	if err != nil || domain.BytesHash(data) != r.AfterHash {
		return "", ErrUnknown
	}
	r.Phase = "operator_reconciled"
	r.Authority += ";operator-confirmed-postimage:" + operator
	r.Result = encode(map[string]any{"ok": true, "status": "reconciled", "path": r.Path, "before_sha256": r.BeforeHash, "after_sha256": r.AfterHash, "edit_receipt_id": r.ID, "source": "operator_reconciled"})
	r, err = w.save(ctx, r, seq+1)
	if err != nil {
		return "", err
	}
	return r.Result, nil
}
func (w *Workspace) Drift(ctx context.Context, receipts []domain.Receipt) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	expected := map[string]string{}
	for _, r := range receipts {
		if r.Tool == "workspace_edit" && r.AfterHash != "" && (strings.Contains(r.Result, `"status":"applied"`) || strings.Contains(r.Result, `"status":"reconciled"`)) {
			expected[r.Path] = r.AfterHash
		}
	}
	paths := []string{}
	for path, hash := range expected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, _, err := w.read(path)
		if err != nil || domain.BytesHash(data) != hash {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}
