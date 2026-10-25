package store

import (
	"context"
	"github.com/Leeminjing/Focus-Go/internal/domain"
)

func (r *Repository) RegisterWorkspace(ctx context.Context, workspace domain.Workspace) error {
	if err := workspace.Validate(); err != nil {
		return err
	}
	return r.update(ctx, func(c *domain.Catalog) (bool, error) {
		if previous, ok := c.Workspaces[workspace.ID]; ok {
			a, _ := domain.Hash(previous)
			b, _ := domain.Hash(workspace)
			if a != b {
				return false, domain.ErrConflict
			}
			return false, nil
		}
		for _, previous := range c.Workspaces {
			if previous.Identity == workspace.Identity {
				return false, domain.ErrConflict
			}
		}
		if c.Workspaces == nil {
			c.Workspaces = map[string]domain.Workspace{}
		}
		c.Workspaces[workspace.ID] = workspace
		c.Version = domain.CodingVersion
		return true, nil
	})
}
