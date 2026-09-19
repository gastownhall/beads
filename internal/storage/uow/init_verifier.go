package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// InitVerifierSource is the capability accessor a unit-of-work provider offers
// for the identity read, the sibling of BootstrapperSource. It is a SEPARATE
// source so that callers which only read — a bts-provisioned team database, an
// authenticating gateway with a read-only credential — cannot reach the write.
type InitVerifierSource interface {
	InitVerifier() (publicops.InitVerifier, error)
}

// initVerifier reads the substrate's identity through a unit of work.
type initVerifier struct {
	provider UnitOfWorkProvider
}

// InitVerifier returns the identity-read surface for this provider.
func (p *doltSQLProvider) InitVerifier() (publicops.InitVerifier, error) {
	return NewInitVerifier(p)
}

// NewInitVerifier constructs a public identity-read surface backed by provider.
func NewInitVerifier(provider UnitOfWorkProvider) (publicops.InitVerifier, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new init verifier: unit-of-work provider must not be nil")
	}
	return &initVerifier{provider: provider}, nil
}

var _ publicops.InitVerifier = (*initVerifier)(nil)

// VerifyIdentity reads both markers inside ONE read transaction, so the pair is
// a snapshot rather than two reads a concurrent bootstrap can land between.
func (v *initVerifier) VerifyIdentity(ctx context.Context, _ publicops.VerifyIdentityRequest) (publicops.VerifyIdentityResult, error) {
	return RunTxRead(ctx, v.provider, func(ctx context.Context, uw UnitOfWork) (publicops.VerifyIdentityResult, error) {
		prefix, projectID, err := readWorkspaceIdentity(ctx, uw.ConfigUseCase())
		if err != nil {
			return publicops.VerifyIdentityResult{}, err
		}
		return publicops.VerifyIdentityResult{Prefix: prefix, ProjectID: projectID}, nil
	})
}

// readWorkspaceIdentity reads the pair the bootstrap refusal and the verifier
// both ask about, so the two cannot disagree about what "identified" means on
// this backend. It is this package's twin of issueops.verifyIdentityInTx.
func readWorkspaceIdentity(ctx context.Context, cfg domain.ConfigUseCase) (prefix, projectID string, err error) {
	prefix, err = cfg.GetConfig(ctx, workapi.ConfigKeyIssuePrefix)
	if err != nil {
		return "", "", err
	}
	projectID, err = cfg.GetMetadata(ctx, workapi.MetadataKeyProjectID)
	if err != nil {
		return "", "", err
	}
	return prefix, projectID, nil
}
