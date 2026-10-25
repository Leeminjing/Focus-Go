package store

import (
	"context"

	"github.com/Leeminjing/Focus-Go/internal/domain"
	"github.com/Leeminjing/go-forest/forest"
)

// Consumers declare their own narrower interfaces; the provider imports no
// app/Agent contract or implementation.
type Capability interface {
	Install(context.Context, domain.Fixture, *domain.Ref) error
	State(context.Context) (domain.Catalog, error)
	Current(context.Context, string, string) (domain.Envelope, error)
	Revision(context.Context, domain.Ref) (domain.Envelope, error)
	Run(context.Context, string) (domain.Run, error)
	Binding(context.Context, string) (domain.Run, domain.Envelope, error)
	Admit(context.Context, domain.Request, string) (domain.Run, error)
	Progress(context.Context, string, string, string, uint64) (domain.Run, error)
	Seal(context.Context, domain.Snapshot) (domain.Snapshot, error)
	Snapshot(context.Context, string) (domain.Snapshot, error)
	Settle(context.Context, string, *string, string) (domain.Run, error)
}

func Definition(directory string) forest.Definition {
	return forest.Definition{TypeID: "focus-go.repository", Version: "1", Capabilities: []forest.CapabilityDescriptor{forest.Capability[Capability]("repository", forest.Public)}, Build: func(ctx context.Context, env *forest.BuildEnv) (forest.Lifecycle, error) {
		repo, err := New(directory)
		if err != nil {
			return nil, err
		}
		return forest.Hooks{}, forest.Provide(env, "repository", repo)
	}}
}
