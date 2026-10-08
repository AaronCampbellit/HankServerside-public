package cloud

import (
	"context"

	"github.com/dropfile/HankServerside/internal/domain"
)

type Authorizer interface {
	RequireHomeFeature(ctx context.Context, home domain.Home, membership domain.HomeMembership, userID string, feature string) error
	RequireAdmin(ctx context.Context, userID string) (domain.Home, domain.HomeMembership, error)
}

type serverAuthorizer struct {
	server *Server
}

func (a serverAuthorizer) RequireHomeFeature(ctx context.Context, home domain.Home, membership domain.HomeMembership, userID string, feature string) error {
	return a.server.requireHomeFeature(ctx, home, membership, userID, feature)
}

func (a serverAuthorizer) RequireAdmin(ctx context.Context, userID string) (domain.Home, domain.HomeMembership, error) {
	return a.server.requireSingletonHomeAdmin(ctx, userID)
}

func (s *Server) Authorizer() Authorizer {
	return serverAuthorizer{server: s}
}
