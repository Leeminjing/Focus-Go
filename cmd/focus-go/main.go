package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/Leeminjing/Focus-Go/internal/app"
	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, diagnostics io.Writer) (result error) {
	if len(args) == 0 || args[0] != "demo" {
		return errors.New("usage: focus-go demo [-data DIRECTORY] [-fixture FILE] [-input ADD=2] [-key demo]")
	}
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	directory := flags.String("data", "data", "Go-only data directory")
	path := flags.String("fixture", "testdata/base-40.json", "Go-owned Context fixture")
	input := flags.String("input", "ADD=2", "new user text")
	key := flags.String("key", "demo", "idempotency key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return domain.ErrInvalid
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxDocument+1))
	file.Close()
	if err != nil {
		return err
	}
	var fixture domain.Fixture
	if err := domain.Strict(data, &fixture); err != nil {
		return err
	}
	if err := fixture.Validate(); err != nil {
		return err
	}
	application, err := app.New(app.Options{Directory: *directory})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Close(ctx); result == nil {
			result = err
		}
	}()
	if previous, err := application.Revision(ctx, fixture.Context.Current); errors.Is(err, domain.ErrNotFound) {
		if err := application.Import(ctx, data, nil); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		a, _ := domain.Hash(previous)
		b, _ := domain.Hash(fixture.Revision)
		if a != b {
			return domain.ErrHash
		}
	}
	request := domain.Request{WorkspaceID: fixture.Context.WorkspaceID, ContextID: fixture.Context.ID, Expected: fixture.Context.Current, Input: *input, Key: *key}
	value, executionErr := application.Run(ctx, request)
	if value.Run.Settlement == nil {
		return errors.Join(executionErr, errors.New("Context Run has not settled"))
	}
	summary := struct {
		Version     string      `json:"contract_version"`
		Input       domain.Ref  `json:"input_revision"`
		Output      *domain.Ref `json:"output_revision"`
		Current     domain.Ref  `json:"current_ref"`
		RunID       string      `json:"run_id"`
		Status      string      `json:"status"`
		Phase       string      `json:"execution_phase"`
		Publication string      `json:"context_publication"`
		EventID     string      `json:"event_id"`
		Answer      string      `json:"answer"`
		Verified    bool        `json:"verified"`
	}{domain.ContractVersion, value.Run.Base, value.Run.Settlement.ContextRevision, value.Current, value.Run.ID, value.Run.Status, value.Run.Phase, value.Run.Settlement.Publication, value.Run.Settlement.EventID, value.Answer, value.Verified}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return err
	}
	return executionErr
}
