package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// VersionReconcilerSource is the capability accessor a unit-of-work provider
// offers for the clone-local version markers, the sibling of
// WorkspaceConfigSource and CounterSource.
type VersionReconcilerSource interface {
	VersionReconciler() (publicops.VersionReconciler, error)
}

// versionReconciler reads and records the version markers through a unit of
// work.
type versionReconciler struct {
	provider UnitOfWorkProvider
}

// VersionReconciler returns the version-marker surface for this provider.
func (p *doltSQLProvider) VersionReconciler() (publicops.VersionReconciler, error) {
	return NewVersionReconciler(p)
}

// NewVersionReconciler constructs a public version-marker surface backed by
// provider.
func NewVersionReconciler(provider UnitOfWorkProvider) (publicops.VersionReconciler, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new version reconciler: unit-of-work provider must not be nil")
	}
	return &versionReconciler{provider: provider}, nil
}

var _ publicops.VersionReconciler = (*versionReconciler)(nil)

func (r *versionReconciler) RecordedVersion(ctx context.Context, _ publicops.RecordedVersionRequest) (publicops.RecordedVersionResult, error) {
	return RunTxRead(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.RecordedVersionResult, error) {
		recorded, mark, err := readVersionMarkers(ctx, uw)
		if err != nil {
			return publicops.RecordedVersionResult{}, err
		}
		return publicops.RecordedVersionResult{Recorded: recorded, HighWaterMark: mark}, nil
	})
}

// ReconcileVersion reads the markers, plans and writes inside ONE unit of work.
//
// The whole decision is inside the transaction on purpose. This backend serves
// a workspace several clients share, so two bd processes can start at the same
// moment against one database; a plan made from a read outside the transaction
// could be written after another process had already moved the marker past it,
// which is the one way this role could ever lower the high-water mark.
//
// VALIDATION HAPPENS BEFORE THE UNIT OF WORK IS OPENED, so a refused
// reconciliation costs no connection and no transaction.
//
// A NO-OP AND A REFUSAL STILL OPEN THE TRANSACTION and commit nothing, which is
// how this backend answers "reconciliation records no history": the commit
// message is only spent when something was written.
func (r *versionReconciler) ReconcileVersion(ctx context.Context, req publicops.VersionReconcileRequest) (publicops.VersionReconcileResult, error) {
	if _, err := workapi.ValidateReconcileVersion(req.CLIVersion); err != nil {
		return publicops.VersionReconcileResult{}, err
	}

	return RunTxResult(ctx, r.provider, func(ctx context.Context, uw UnitOfWork) (publicops.VersionReconcileResult, string, error) {
		recorded, mark, err := readVersionMarkers(ctx, uw)
		if err != nil {
			return publicops.VersionReconcileResult{}, "", err
		}
		plan, err := workapi.PlanVersionReconcile(req.CLIVersion, recorded, mark)
		if err != nil {
			return publicops.VersionReconcileResult{}, "", err
		}

		if plan.RecordVersion {
			if err := uw.ConfigUseCase().SetLocalMetadata(ctx, workapi.MetadataKeyVersion, plan.Result.Current); err != nil {
				return publicops.VersionReconcileResult{}, "", err
			}
		}
		if plan.RecordHighWaterMark {
			if err := uw.ConfigUseCase().SetLocalMetadata(ctx, workapi.MetadataKeyVersionMax, plan.Result.Current); err != nil {
				return publicops.VersionReconcileResult{}, "", err
			}
		}
		return plan.Result, "bd: reconcile version -> " + plan.Result.Current, nil
	})
}

func readVersionMarkers(ctx context.Context, uw UnitOfWork) (recorded, mark string, err error) {
	recorded, err = uw.ConfigUseCase().GetLocalMetadata(ctx, workapi.MetadataKeyVersion)
	if err != nil {
		return "", "", err
	}
	mark, err = uw.ConfigUseCase().GetLocalMetadata(ctx, workapi.MetadataKeyVersionMax)
	if err != nil {
		return "", "", err
	}
	return recorded, mark, nil
}
