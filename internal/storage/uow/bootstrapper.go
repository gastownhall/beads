package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// BootstrapperSource is the capability accessor a unit-of-work provider offers
// for identity seeding, the sibling of InitVerifierSource and
// VersionReconcilerSource.
type BootstrapperSource interface {
	Bootstrapper() (publicops.Bootstrapper, error)
}

// bootstrapper seeds a substrate's identity through a unit of work.
type bootstrapper struct {
	provider UnitOfWorkProvider
}

// Bootstrapper returns the identity-seeding surface for this provider.
func (p *doltSQLProvider) Bootstrapper() (publicops.Bootstrapper, error) {
	return NewBootstrapper(p)
}

// NewBootstrapper constructs a public identity-seeding surface backed by
// provider.
func NewBootstrapper(provider UnitOfWorkProvider) (publicops.Bootstrapper, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new bootstrapper: unit-of-work provider must not be nil")
	}
	return &bootstrapper{provider: provider}, nil
}

var _ publicops.Bootstrapper = (*bootstrapper)(nil)

// Bootstrap reads the identity, refuses over an identified substrate and
// writes the new one inside ONE unit of work.
//
// THE REFUSAL IS INSIDE THE TRANSACTION, and this is the backend where that
// matters most: it serves a workspace several clients share, so two inits can
// arrive at one database at the same moment, and a refusal decided from a read
// outside the transaction is one both of them pass.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK IS OPENED — a refused bootstrap
// should cost no connection and no transaction.
//
// ONE VERSION-CONTROL ENTRY, which is what the role's "at most one" permits:
// one entry rather than one per key.
//
// The per-clone bookkeeping `bd init` seeds alongside the identity is not here
// and not on the role; see issueops.Bootstrapper for why.
func (b *bootstrapper) Bootstrap(ctx context.Context, req publicops.BootstrapRequest) (publicops.BootstrapResult, error) {
	req, err := workapi.ValidateBootstrapRequest(req)
	if err != nil {
		return publicops.BootstrapResult{}, err
	}

	return RunTxResult(ctx, b.provider, func(ctx context.Context, uw UnitOfWork) (publicops.BootstrapResult, string, error) {
		cfg := uw.ConfigUseCase()
		prefix, projectID, err := readWorkspaceIdentity(ctx, cfg)
		if err != nil {
			return publicops.BootstrapResult{}, "", err
		}
		if err := workapi.RefuseIdentifiedSubstrate(prefix, projectID); err != nil {
			return publicops.BootstrapResult{}, "", err
		}

		if err := cfg.SetConfig(ctx, workapi.ConfigKeyIssuePrefix, req.Prefix); err != nil {
			return publicops.BootstrapResult{}, "", err
		}
		if err := cfg.SetMetadata(ctx, workapi.MetadataKeyProjectID, req.ProjectID); err != nil {
			return publicops.BootstrapResult{}, "", err
		}

		result := publicops.BootstrapResult{Prefix: req.Prefix, ProjectID: req.ProjectID}
		return result, "bd: bootstrap " + req.Prefix, nil
	})
}
