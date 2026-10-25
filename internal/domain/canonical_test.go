package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func TestCanonicalHashMatchesIndependentPythonGoldens(t *testing.T) {
	data, err := os.ReadFile("../../testdata/hash-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Input, Canonical, SHA256 string }
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		canonical, err := domain.Canonical([]byte(row.Input))
		if err != nil || string(canonical) != row.Canonical {
			t.Fatal(string(canonical), row.Canonical, err)
		}
		got, err := domain.HashJSON([]byte(row.Input))
		if err != nil || got != row.SHA256 {
			t.Fatal(got, row.SHA256, err)
		}
	}
}

func TestContractRejectsAmbiguousJSONInsteadOfNormalizingIt(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"a":1,"a":2}`), []byte(`1.0`), []byte(`1e0`), []byte(`9007199254740992`), []byte(`"\ud800"`), []byte(`{} {}`), {'"', 0xff, '"'}} {
		if _, err := domain.Canonical(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestTypedContractsDoNotNormalizeNullIntoAnEmptyIdentityField(t *testing.T) {
	var required struct {
		Namespace string `json:"namespace"`
	}
	if err := domain.Strict([]byte(`{"namespace":null}`), &required); err == nil {
		t.Fatal("null silently became an empty string")
	}
	if err := domain.Strict([]byte(`{"Namespace":null}`), &required); err == nil {
		t.Fatal("case alias bypassed strict field identity")
	}
	var optional struct {
		Namespace *string `json:"namespace"`
	}
	if err := domain.Strict([]byte(`{"namespace":null}`), &optional); err != nil {
		t.Fatal(err)
	}
}
