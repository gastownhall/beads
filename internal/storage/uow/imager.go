package uow

import (
	"context"
	"fmt"

	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

// ImagerSource is the capability accessor a unit-of-work provider offers for
// the raw bead-image role, the sibling of DeleterSource.
type ImagerSource interface {
	Imager() (publicops.Imager, error)
}

// imager reads bead images through a unit of work.
type imager struct {
	provider UnitOfWorkProvider
}

// Imager returns the raw bead-image surface for this provider.
func (p *doltSQLProvider) Imager() (publicops.Imager, error) {
	return NewImager(p)
}

// Imager builds on the notifying provider itself, as every role accessor
// does. A read records no hook, so nothing is buffered either way.
func (p *notifyingProvider) Imager() (publicops.Imager, error) { return NewImager(p) }

// NewImager constructs a public imager backed by provider.
func NewImager(provider UnitOfWorkProvider) (publicops.Imager, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new imager: unit-of-work provider must not be nil")
	}
	return &imager{provider: provider}, nil
}

var (
	_ publicops.Imager = (*imager)(nil)
	_ ImagerSource     = (*doltSQLProvider)(nil)
	_ ImagerSource     = (*notifyingProvider)(nil)
)

// Image reads every id's image inside ONE read-only unit of work.
//
// Unlike Deleter, this is not a separate body over the domain use cases: an
// image is every raw row in six tables, which no use case returns, and two
// bodies would be two definitions of the revision. It runs the stores'
// issueops.ImageInTx on the unit of work's statement runner instead, so all
// three routes hash the same bytes.
func (m *imager) Image(ctx context.Context, ids []string) ([]publicops.Image, error) {
	return RunTxRead(ctx, m.provider, func(ctx context.Context, uw UnitOfWork) ([]publicops.Image, error) {
		runner, err := imageStatementRunner(uw)
		if err != nil {
			return nil, err
		}
		return storeops.ImageInTx(ctx, runner, ids)
	})
}

// imageStatementRunner reaches the unit of work's statement runner past any
// decorator, as importStatementRunner does. The reads never pass a use case,
// which costs nothing here: a read fires no hook.
func imageStatementRunner(uw UnitOfWork) (storeops.DBTX, error) {
	b, ok := unwrapUOW(uw).(*baseUOW)
	if !ok {
		return nil, fmt.Errorf("uow: imager: %T does not expose a statement runner", uw)
	}
	return b.tx.Runner(), nil
}
