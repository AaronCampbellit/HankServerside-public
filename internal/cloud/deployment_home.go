package cloud

import (
	"context"
	"errors"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

type DeploymentHomeResolver struct {
	store *store.Store
}

func NewDeploymentHomeResolver(db *store.Store) DeploymentHomeResolver {
	return DeploymentHomeResolver{store: db}
}

func (r DeploymentHomeResolver) Resolve(ctx context.Context) (domain.Home, error) {
	count, err := r.store.CountHomes(ctx)
	if err != nil {
		return domain.Home{}, err
	}
	switch count {
	case 0:
		return domain.Home{}, store.ErrNotFound
	case 1:
		return r.store.GetSingletonHome(ctx)
	default:
		return domain.Home{}, store.ErrUnsupportedMultiHome
	}
}

func (r DeploymentHomeResolver) ResolveForUser(ctx context.Context, userID string) (domain.Home, domain.HomeMembership, error) {
	home, err := r.Resolve(ctx)
	if err != nil {
		return domain.Home{}, domain.HomeMembership{}, err
	}
	membership, err := r.store.GetHomeMembership(ctx, home.ID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Home{}, domain.HomeMembership{}, store.ErrNotFound
		}
		return domain.Home{}, domain.HomeMembership{}, err
	}
	return home, membership, nil
}
