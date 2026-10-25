package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Leeminjing/Focus-Go/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceAndContextCommandsNeedNoModelCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	directory := t.TempDir()
	root := t.TempDir()
	text := filepath.Join(t.TempDir(), "context.txt")
	if err := os.WriteFile(text, []byte("Maintain this project."), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"workspace", "bind", "-data", directory, "-id", "w", "-root", root, "-read", ".", "-write", "calc.go"}, &output, &diagnostics); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	output.Reset()
	if err := run(context.Background(), []string{"context", "create", "-data", directory, "-workspace", "w", "-id", "c", "-text-file", text}, &output, &diagnostics); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	var ref domain.Ref
	if json.Unmarshal(output.Bytes(), &ref) != nil || ref.Validate() != nil || ref.Generation != 1 {
		t.Fatal(output.String())
	}
	output.Reset()
	if err := run(context.Background(), []string{"context", "show", "-data", directory, "-workspace", "w", "-id", "c"}, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var envelope domain.Envelope
	if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.Validate() != nil {
		t.Fatal(output.String())
	}
}
