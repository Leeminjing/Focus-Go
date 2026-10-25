package domain_test

import (
	"errors"
	"os"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func TestFrozenFixturePreservesHistoryAndRejectsWrongHashOrBackend(t *testing.T) {
	data, err := os.ReadFile("../../testdata/base-40.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture domain.Fixture
	if err := domain.Strict(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Validate(); err != nil {
		t.Fatal(err)
	}
	messages, err := fixture.Revision.Revision.History.Messages()
	if err != nil || len(messages) != 2 || messages[0].Content != "BASE=40" {
		t.Fatal(messages, err)
	}
	bad, _ := domain.Copy(fixture)
	bad.Revision.Revision.History.Execution[0].Origin = "runtime"
	if err := bad.Validate(); !errors.Is(err, domain.ErrUnsupportedHistory) {
		t.Fatal(err)
	}
	bad, _ = domain.Copy(fixture)
	bad.Revision.Revision.ContentHash = "wrong"
	if err := bad.Validate(); !errors.Is(err, domain.ErrHash) {
		t.Fatal(err)
	}
	bad, _ = domain.Copy(fixture)
	bad.Revision.Backend.Engine = "langgraph"
	if err := bad.Validate(); !errors.Is(err, domain.ErrUnsupportedCheckpoint) {
		t.Fatal(err)
	}
}
