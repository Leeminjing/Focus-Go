package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDemoCommandProducesAndReusesARealContextRevision(t *testing.T) {
	directory := t.TempDir()
	args := []string{"demo", "-data", directory, "-fixture", filepath.Join("..", "..", "testdata", "base-40.json")}
	var original []byte
	for attempt := 0; attempt < 2; attempt++ {
		var out, diagnostics bytes.Buffer
		if err := run(context.Background(), args, &out, &diagnostics); err != nil {
			t.Fatal(err, diagnostics.String())
		}
		var result struct {
			Answer      string `json:"answer"`
			Verified    bool   `json:"verified"`
			Publication string `json:"context_publication"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Answer != "42" || !result.Verified || result.Publication != "published" {
			t.Fatal(out.String(), err)
		}
		if attempt == 0 {
			original = append([]byte(nil), out.Bytes()...)
		} else if !bytes.Equal(original, out.Bytes()) {
			t.Fatal("idempotent CLI changed settlement", string(original), out.String())
		}
	}
}
