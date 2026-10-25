package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest-agent/agent"
)

// The deterministic model derives arguments from actual history and input.
type fixtureModel struct{}

func (fixtureModel) Generate(ctx context.Context, request agent.ModelRequest, emit func(string) error) (agent.ModelResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ModelResult{}, err
	}
	if len(request.Messages) == 0 {
		return agent.ModelResult{}, domain.ErrInvalid
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == "tool" {
		if err := emit(last.Content); err != nil {
			return agent.ModelResult{}, err
		}
		return agent.ModelResult{Content: last.Content}, nil
	}
	var base int64
	found := false
	for _, message := range request.Messages[:len(request.Messages)-1] {
		if message.Role == "user" && strings.HasPrefix(message.Content, "BASE=") {
			var err error
			base, err = parseInteger(strings.TrimPrefix(message.Content, "BASE="))
			if err != nil {
				return agent.ModelResult{}, err
			}
			found = true
		}
	}
	if !found || last.Role != "user" || !strings.HasPrefix(last.Content, "ADD=") {
		return agent.ModelResult{}, domain.ErrInvalid
	}
	add, err := parseInteger(strings.TrimPrefix(last.Content, "ADD="))
	if err != nil {
		return agent.ModelResult{}, err
	}
	args, err := domain.Marshal(struct {
		A int64 `json:"a"`
		B int64 `json:"b"`
	}{base, add})
	if err != nil {
		return agent.ModelResult{}, err
	}
	return agent.ModelResult{Calls: []agent.ToolCall{{ID: domain.StableID("call", request.RunID, 0), Name: "add", Arguments: args}}}, nil
}
func parseInteger(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > domain.MaxInteger || n < -domain.MaxInteger || strconv.FormatInt(n, 10) != s {
		return 0, domain.ErrInvalid
	}
	return n, nil
}

type addition struct{ approval bool }

func (a addition) Describe(context.Context) (agent.ToolSpec, error) {
	return agent.ToolSpec{Name: "add", Version: "1", Description: "Add two safe integers", Parameters: []byte(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"],"additionalProperties":false}`), NeedsApproval: a.approval}, nil
}
func (addition) Execute(ctx context.Context, request agent.ToolRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args struct {
		A *int64 `json:"a"`
		B *int64 `json:"b"`
	}
	if domain.Strict(request.Call.Arguments, &args) != nil || args.A == nil || args.B == nil {
		return "", domain.ErrInvalid
	}
	sum := *args.A + *args.B
	if sum > domain.MaxInteger || sum < -domain.MaxInteger {
		return "", domain.ErrInvalid
	}
	return fmt.Sprint(sum), nil
}
