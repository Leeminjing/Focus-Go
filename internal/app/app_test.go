package app_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func TestContextRunPublishesVerifiableRevisionAndReplaysSettlement(t *testing.T) {
	for _, example := range []struct{ base, add, answer int }{{40, 2, 42}, {7, 5, 12}} {
		t.Run(fmt.Sprint(example.base), func(t *testing.T) {
			ctx := context.Background()
			application, err := app.New(app.Options{Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer application.Close(ctx)
			data, err := os.ReadFile(fmt.Sprintf("../../testdata/base-%d.json", example.base))
			if err != nil {
				t.Fatal(err)
			}
			var fixture domain.Fixture
			if err := domain.Strict(data, &fixture); err != nil {
				t.Fatal(err)
			}
			if err := application.Import(ctx, data, nil); err != nil {
				t.Fatal(err)
			}
			request := domain.Request{WorkspaceID: "w1", ContextID: "c1", Expected: fixture.Context.Current, Input: fmt.Sprintf("ADD=%d", example.add), Key: "request-1"}
			result, err := application.Run(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.Status != "success" || result.Run.Settlement == nil || result.Run.Settlement.Publication != "published" || result.Answer != fmt.Sprint(example.answer) {
				t.Fatal(result)
			}
			current, err := application.Current(ctx, "w1", "c1")
			if err != nil || current.Validate() != nil || current.Revision.Ref.Generation != 2 || len(current.Revision.Sources) != 1 || !current.Revision.Sources[0].Source.Equal(request.Expected) {
				t.Fatal(current, err)
			}
			if len(current.Revision.History.Authored) != 2 || len(current.Revision.History.Execution) != 6 {
				t.Fatal("history partitions", current.Revision.History)
			}
			original, err := application.Revision(ctx, request.Expected)
			if err != nil || original.Revision.ContentHash != fixture.Revision.Revision.ContentHash {
				t.Fatal(original, err)
			}
			again, err := application.Run(ctx, request)
			if err != nil || again.Run.ID != result.Run.ID || again.Run.Settlement.EventID != result.Run.Settlement.EventID || !again.Run.Settlement.ContextRevision.Equal(*result.Run.Settlement.ContextRevision) {
				t.Fatal(again, err)
			}
			state, err := application.State(ctx)
			if err != nil || len(state.Revisions) != 2 || len(state.Runs) != 1 || len(state.Events) != 1 {
				t.Fatal(state, err)
			}
		})
	}
}
