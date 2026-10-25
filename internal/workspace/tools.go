package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
)

type Authorization struct {
	RunID         string
	Workspace     domain.Workspace
	Approved      bool
	AgentRevision uint64
}
type Authorize func(context.Context, agent.ToolRequest) (Authorization, error)
type tool struct {
	kind      string
	resource  *Workspace
	authorize Authorize
}

func NewTool(kind string, resource *Workspace, authorize Authorize) agent.Tool {
	return &tool{kind, resource, authorize}
}

func Schema(kind string, preapproved bool) (agent.ToolSpec, error) {
	properties := map[string]any{}
	required := []string{}
	description := ""
	field := func(name, kind string) {
		properties[name] = map[string]string{"type": kind}
		required = append(required, name)
	}
	switch kind {
	case "workspace_search":
		field("mode", "string")
		field("path_prefix", "string")
		field("query", "string")
		field("limit", "integer")
		description = "Find authorized workspace files (mode files) or literal text (mode text). Use an empty prefix for the workspace. Results are bounded."
	case "workspace_read":
		field("path", "string")
		field("start_line", "integer")
		field("line_count", "integer")
		description = "Read an authorized UTF-8 file range. Returns the whole-file SHA256 and a durable read receipt required for editing."
	case "workspace_edit":
		field("path", "string")
		field("expected_sha256", "string")
		field("read_receipt_id", "string")
		field("old_text", "string")
		field("new_text", "string")
		description = "Replace one exact unique text span in an existing file. Requires a matching read receipt and whole-file SHA256; never creates or deletes files."
	default:
		return agent.ToolSpec{}, domain.ErrInvalid
	}
	parameters, err := domain.Marshal(map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false})
	if err != nil {
		return agent.ToolSpec{}, err
	}
	return agent.ToolSpec{Name: kind, Version: "1", Description: description, Parameters: parameters, NeedsApproval: kind == "workspace_edit" && !preapproved}, nil
}
func (t *tool) Describe(ctx context.Context) (agent.ToolSpec, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolSpec{}, err
	}
	return Schema(t.kind, t.resource.binding.Preapproved)
}

type arguments struct {
	Path        string `json:"path,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	LineCount   int    `json:"line_count,omitempty"`
	Expected    string `json:"expected_sha256,omitempty"`
	ReadReceipt string `json:"read_receipt_id,omitempty"`
	Old         string `json:"old_text,omitempty"`
	New         string `json:"new_text,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Prefix      string `json:"path_prefix,omitempty"`
	Query       string `json:"query,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func (t *tool) parse(raw json.RawMessage) (arguments, error) {
	var args arguments
	var object map[string]json.RawMessage
	if len(raw) > 128<<10 || domain.Strict(raw, &object) != nil || object == nil || domain.Strict(raw, &args) != nil {
		return args, domain.ErrInvalid
	}
	spec, _ := t.Describe(context.Background())
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
		Type       string                     `json:"type"`
		Additional bool                       `json:"additionalProperties"`
	}
	if domain.Strict(spec.Parameters, &schema) != nil {
		return args, domain.ErrInvalid
	}
	if len(object) != len(schema.Required) {
		return args, domain.ErrInvalid
	}
	for _, key := range schema.Required {
		if _, ok := object[key]; !ok {
			return args, domain.ErrInvalid
		}
	}
	for key := range object {
		if _, ok := schema.Properties[key]; !ok {
			return args, domain.ErrInvalid
		}
	}
	return args, nil
}
func (t *tool) Execute(ctx context.Context, request agent.ToolRequest) (string, error) {
	if t.resource == nil || t.authorize == nil || !domain.ID(request.RunID) || !domain.ID(request.Call.ID) || request.Call.Name != t.kind {
		return "", domain.ErrInvalid
	}
	auth, err := t.authorize(ctx, request)
	if err != nil {
		return "", err
	}
	w := t.resource
	w.mu.Lock()
	defer w.mu.Unlock()
	grant, err := domain.Hash(auth.Workspace)
	if err != nil {
		return "", err
	}
	expectedGrant, _ := domain.Hash(w.binding)
	if !domain.ID(auth.RunID) || grant != expectedGrant {
		return "", ErrDenied
	}
	argsHash, err := domain.HashJSON(request.Call.Arguments)
	if err != nil {
		return "", domain.ErrInvalid
	}
	key, _ := domain.Hash(struct{ Run, Agent, Call string }{auth.RunID, request.RunID, request.Call.ID})
	id := domain.StableID("receipt", key, 0)
	old, _, err := w.load(ctx, request.RunID, id)
	if err == nil {
		if old.ArgumentsHash != argsHash || old.GrantHash != grant {
			return known("conflict"), nil
		}
		if old.Phase == "prepared" {
			return "", ErrUnknown
		}
		return old.Result, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	receipts, err := w.Receipts(ctx, request.RunID)
	if err != nil {
		return "", err
	}
	if len(receipts) >= 64 {
		return "", domain.ErrInvalid
	}
	r := domain.Receipt{Version: domain.CodingVersion, ID: id, RunID: auth.RunID, AgentRunID: request.RunID, CallID: request.Call.ID, Tool: t.kind, ToolVersion: "1", Arguments: request.Call.Arguments, ArgumentsHash: argsHash, GrantHash: grant, Authority: fmt.Sprintf("agent-revision:%d;approved:%t", auth.AgentRevision, auth.Approved), Phase: "completed"}
	finish := func(result string) (string, error) {
		r.Result = result
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		saved, err := w.save(saveCtx, r, 1)
		if err != nil {
			return "", err
		}
		if err = w.notify(ReceiptSaved, saved); err != nil {
			return "", err
		}
		return result, nil
	}
	args, err := t.parse(request.Call.Arguments)
	if err != nil {
		return finish(known("invalid_arguments"))
	}
	r.Path = args.Path
	if err = w.checkContext(ctx); err != nil {
		return "", err
	}
	budget := 0
	for _, receipt := range receipts {
		budget += len(receipt.Before) + len(receipt.After) + len(receipt.Result)
	}
	if budget > 7<<20 {
		return finish(known("evidence_limit"))
	}
	switch t.kind {
	case "workspace_search":
		result, err := w.search(ctx, args)
		if err != nil {
			return finish(known("denied_or_limit"))
		}
		return finish(result)
	case "workspace_read":
		if args.StartLine < 1 || args.LineCount < 1 || args.LineCount > 200 {
			return finish(known("invalid_range"))
		}
		data, _, err := w.read(args.Path)
		if err != nil {
			return finish(known("unavailable"))
		}
		if budget+len(data) > 7<<20 {
			return finish(known("evidence_limit"))
		}
		start, end, lineEnd := lineRange(data, args.StartLine, args.LineCount)
		if start < 0 {
			return finish(known("invalid_range"))
		}
		if end-start > 64<<10 {
			end = start + 64<<10
			for end > start && (data[end]&0xc0) == 0x80 {
				end--
			}
			lineEnd = args.StartLine + strings.Count(string(data[start:end]), "\n")
		}
		r.Before = string(data)
		r.BeforeHash = domain.BytesHash(data)
		r.Start = start
		r.End = end
		return finish(encode(map[string]any{"ok": true, "status": "ok", "path": args.Path, "sha256": r.BeforeHash, "bytes": len(data), "start_line": args.StartLine, "end_line": lineEnd, "content": string(data[start:end]), "truncated": end < len(data), "read_receipt_id": r.ID}))
	case "workspace_edit":
		if !auth.Approved && !w.binding.Preapproved {
			return finish(known("approval_required"))
		}
		if !validPath(args.Path) || !allowed(w.binding.Write, args.Path) || args.Old == "" {
			return finish(known("denied"))
		}
		read, _, err := w.load(ctx, request.RunID, args.ReadReceipt)
		if err != nil || read.Tool != "workspace_read" || read.RunID != auth.RunID || read.Path != args.Path || read.BeforeHash != args.Expected || read.GrantHash != grant || read.Phase != "completed" {
			return finish(known("invalid_read_receipt"))
		}
		data, info, err := w.read(args.Path)
		if err != nil {
			return finish(known("unavailable"))
		}
		if domain.BytesHash(data) != args.Expected {
			return finish(known("version_conflict"))
		}
		if strings.Count(string(data), args.Old) != 1 {
			return finish(known("ambiguous_edit"))
		}
		offset := strings.Index(string(data), args.Old)
		if offset < read.Start || offset+len(args.Old) > read.End {
			return finish(known("unread_span"))
		}
		after := strings.Replace(string(data), args.Old, args.New, 1)
		if len(after) > maxFile || strings.ContainsRune(after, 0) || budget+len(data)+len(after) > 7<<20 {
			return finish(known("evidence_limit"))
		}
		r.Before = string(data)
		r.After = after
		r.BeforeHash = domain.BytesHash(data)
		r.AfterHash = domain.BytesHash([]byte(after))
		r.Start = offset
		r.End = offset + len(args.Old)
		r.Phase = "prepared"
		if r, err = w.save(ctx, r, 1); err != nil {
			return "", err
		}
		if err = w.notify(Prepared, r); err != nil {
			return "", err
		}
		name, err := tempName(args.Path)
		if err != nil {
			return "", err
		}
		defer w.root.Remove(name)
		file, err := w.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return "", err
		}
		if _, err = file.Write([]byte(after)); err != nil {
			file.Close()
			return "", err
		}
		if err = file.Sync(); err != nil {
			file.Close()
			return "", err
		}
		if err = file.Close(); err != nil {
			return "", err
		}
		current, currentInfo, err := w.read(args.Path)
		if err != nil || domain.BytesHash(current) != r.BeforeHash || !os.SameFile(info, currentInfo) {
			r.Phase = "completed"
			r.Result = known("version_conflict")
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, saveErr := w.save(saveCtx, r, 2); saveErr != nil {
				return "", saveErr
			}
			return r.Result, nil
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if err = w.root.Rename(name, args.Path); err != nil {
			return "", err
		}
		if err = w.notify(FileReplaced, r); err != nil {
			return "", err
		}
		actual, _, err := w.read(args.Path)
		if err != nil || domain.BytesHash(actual) != r.AfterHash {
			return "", ErrUnknown
		}
		if err = w.syncDir(args.Path); err != nil {
			return "", err
		}
		r.Phase = "completed"
		r.Result = encode(map[string]any{"ok": true, "status": "applied", "path": r.Path, "before_sha256": r.BeforeHash, "after_sha256": r.AfterHash, "edit_receipt_id": r.ID})
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if r, err = w.save(saveCtx, r, 2); err != nil {
			return "", err
		}
		if err = w.notify(ReceiptSaved, r); err != nil {
			return "", err
		}
		return r.Result, nil
	}
	return "", domain.ErrInvalid
}
func known(code string) string {
	return encode(map[string]any{"ok": false, "status": "rejected", "code": code})
}
func encode(value any) string {
	data, err := domain.Marshal(value)
	if err != nil {
		return `{"ok":false,"status":"rejected","code":"invalid_result"}`
	}
	return string(data)
}
func lineRange(data []byte, start, count int) (int, int, int) {
	position, line := 0, 1
	for position < len(data) && line < start {
		if data[position] == '\n' {
			line++
		}
		position++
	}
	if line != start || (position == len(data) && len(data) > 0) {
		return -1, 0, 0
	}
	begin := position
	for position < len(data) {
		position++
		if data[position-1] == '\n' {
			line++
			if line == start+count {
				break
			}
		}
	}
	endLine := line
	if position > begin && data[position-1] == '\n' {
		endLine--
	}
	return begin, position, endLine
}
func (w *Workspace) search(ctx context.Context, args arguments) (string, error) {
	if args.Limit < 1 || args.Limit > 100 || (args.Mode != "files" && args.Mode != "text") || (args.Mode == "text" && args.Query == "") || (args.Prefix != "" && !validPath(strings.TrimSuffix(args.Prefix, "/"))) {
		return "", domain.ErrInvalid
	}
	paths := []string{}
	count := 0
	var walk func(string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := w.root.Open(dir)
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(10001 - count)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		count += len(entries)
		if count > 10000 {
			return domain.ErrInvalid
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			path := entry.Name()
			if dir != "." {
				path = dir + "/" + path
			}
			if !validPath(path) {
				continue
			}
			info, err := w.root.Lstat(path)
			if err != nil || linked(info) {
				continue
			}
			if entry.IsDir() {
				visit := false
				for _, rule := range w.binding.Read {
					if rule == "." || strings.HasPrefix(rule, path+"/") || (strings.HasSuffix(rule, "/") && strings.HasPrefix(path+"/", rule)) {
						visit = true
					}
				}
				if visit {
					if err := walk(path); err != nil {
						return err
					}
				}
				continue
			}
			if info.Mode().IsRegular() && allowed(w.binding.Read, path) && (args.Prefix == "" || path == args.Prefix || strings.HasPrefix(path, strings.TrimSuffix(args.Prefix, "/")+"/")) {
				file, err := w.root.Open(path)
				if err != nil {
					continue
				}
				actual, err := file.Stat()
				if err != nil {
					file.Close()
					continue
				}
				_, links, err := identity(file, actual)
				file.Close()
				if err != nil || links != 1 || !actual.Mode().IsRegular() {
					continue
				}
				paths = append(paths, path)
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return "", err
	}
	sort.Strings(paths)
	matches := []map[string]any{}
	bytes := 0
	truncated := false
	for _, path := range paths {
		if len(matches) >= args.Limit {
			truncated = true
			break
		}
		if args.Mode == "files" {
			matches = append(matches, map[string]any{"path": path})
			continue
		}
		data, _, err := w.read(path)
		if err != nil {
			continue
		}
		bytes += len(data)
		if bytes > 16<<20 {
			truncated = true
			break
		}
		for index, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, args.Query) {
				snippet := line
				if len(snippet) > 1024 {
					snippet = snippet[:1024]
					for !utf8.ValidString(snippet) {
						snippet = snippet[:len(snippet)-1]
					}
				}
				matches = append(matches, map[string]any{"path": path, "line": index + 1, "text": snippet})
				if len(matches) >= args.Limit {
					truncated = true
					break
				}
			}
		}
	}
	return encode(map[string]any{"ok": true, "status": "ok", "matches": matches, "truncated": truncated}), nil
}
