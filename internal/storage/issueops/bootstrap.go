package issueops

import (
	"context"

	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the store-backed bodies behind issueops.Bootstrapper and
// issueops.InitVerifier.
//
// They live here rather than in an importable internal/workapi/store<role>
// package for the reason SweepInTx and DetectCycleReportInTx do: the work is a
// read and the write it QUALIFIES, and those two must see one snapshot or the
// refusal is a suggestion. The two Dolt-backed stores share these bodies and
// differ only in how they reach a transaction, so they are ONE vote and the
// unit-of-work provider is the second.

// BootstrapInTx seeds an unidentified substrate's identity, refusing an
// identified one, inside ONE transaction.
//
// It assumes a request already normalized and refused by
// workapi.ValidateBootstrapRequest, which the accessors run BEFORE opening a
// transaction.
//
// THE READ AND THE WRITE SHARE THE TRANSACTION, which is the whole reason this
// body is shaped as a function over a *sql.Tx: two inits racing against one
// shared database is the ordinary case for a bts-provisioned server, and a
// refusal decided outside the transaction that writes is one both racers pass.
//
// The per-clone bookkeeping `bd init` seeds alongside the identity is not here
// and not on the role; see issueops.Bootstrapper for why.
func BootstrapInTx(ctx context.Context, tx DBTX, req publicops.BootstrapRequest) (publicops.BootstrapResult, error) {
	prefix, projectID, err := verifyIdentityInTx(ctx, tx)
	if err != nil {
		return publicops.BootstrapResult{}, err
	}
	if err := workapi.RefuseIdentifiedSubstrate(prefix, projectID); err != nil {
		return publicops.BootstrapResult{}, err
	}

	if err := SetConfigInTx(ctx, tx, workapi.ConfigKeyIssuePrefix, req.Prefix); err != nil {
		return publicops.BootstrapResult{}, err
	}
	if err := SetMetadataInTx(ctx, tx, workapi.MetadataKeyProjectID, req.ProjectID); err != nil {
		return publicops.BootstrapResult{}, err
	}

	return publicops.BootstrapResult{Prefix: req.Prefix, ProjectID: req.ProjectID}, nil
}

// VerifyIdentityInTx reads the substrate's identity inside one transaction.
func VerifyIdentityInTx(ctx context.Context, tx DBTX) (publicops.VerifyIdentityResult, error) {
	prefix, projectID, err := verifyIdentityInTx(ctx, tx)
	if err != nil {
		return publicops.VerifyIdentityResult{}, err
	}
	return publicops.VerifyIdentityResult{Prefix: prefix, ProjectID: projectID}, nil
}

// verifyIdentityInTx reads the pair the bootstrap refusal and the verifier both
// ask about, so the two cannot disagree about what "identified" means. Both
// reads happen even when the first already decides the outcome: the verifier
// publishes the pair, and the refusal names both values.
func verifyIdentityInTx(ctx context.Context, tx DBTX) (prefix, projectID string, err error) {
	prefix, err = GetConfigInTx(ctx, tx, workapi.ConfigKeyIssuePrefix)
	if err != nil {
		return "", "", err
	}
	projectID, err = GetMetadataInTx(ctx, tx, workapi.MetadataKeyProjectID)
	if err != nil {
		return "", "", err
	}
	return prefix, projectID, nil
}
