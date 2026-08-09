package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/issueops"
)

// This file holds the shared, database-free half of issueops.Bootstrapper and
// issueops.InitVerifier: the keys the identity lives under, the request
// validation, and the normalization that makes the prefix a bootstrap stores
// the same string on every backend. It is pure so that three implementations
// decide through one body and the refusals pin without a database.

// The two keys a workspace's IDENTITY lives under.
//
// They are named here rather than spelled at each call site because a key
// spelled differently in one of the places that read them is an identity nothing
// else can find — which looks exactly like a database nobody ever initialized.
//
// These two and no others decide whether a substrate is bootstrapped at all,
// and either one of them present is what Bootstrapper.Bootstrap refuses over.
const (
	// ConfigKeyIssuePrefix lives on the durable settings plane, which is where
	// every other reader of the prefix already looks for it.
	ConfigKeyIssuePrefix = "issue_prefix"
	// MetadataKeyProjectID is the identity cross-project verification compares
	// on connection. The leading underscore is historical and load-bearing:
	// it is the spelling every existing workspace already carries.
	MetadataKeyProjectID = "_project_id"
)

// NormalizeBootstrapPrefix returns the prefix as a bootstrap STORES it.
//
// The settings plane strips a trailing hyphen from this key. Doing it here
// instead of relying on that means BootstrapResult.Prefix and the value a later
// VerifyIdentity answers with are one value on every backend, including the
// unit-of-work one whose write does not pass through that plane's normalizer.
func NormalizeBootstrapPrefix(prefix string) string {
	return strings.TrimRight(prefix, "-")
}

// ValidateBootstrapRequest refuses a bootstrap that cannot produce a usable
// workspace and returns the request as it will be WRITTEN.
//
// Every implementation validates through it BEFORE opening a transaction, which
// is what makes issueops.Bootstrapper's "a refusal writes nothing" true of the
// connection as well as of the keys.
func ValidateBootstrapRequest(req issueops.BootstrapRequest) (issueops.BootstrapRequest, error) {
	req.Prefix = NormalizeBootstrapPrefix(req.Prefix)
	if req.Prefix == "" {
		return issueops.BootstrapRequest{}, fmt.Errorf("%w: bootstrap requires a non-empty issue prefix", issueops.ErrValidation)
	}
	if req.ProjectID == "" {
		return issueops.BootstrapRequest{}, fmt.Errorf("%w: bootstrap requires a project id; this role does not mint one", issueops.ErrValidation)
	}
	return req, nil
}

// RefuseIdentifiedSubstrate turns the pair a bootstrap read into the refusal
// issueops.Bootstrapper promises, or nil when the substrate is free.
//
// EITHER MARKER IS ENOUGH. A substrate several rigs share carries a prefix one
// of them chose, and a bootstrap that overwrote it would rename every id the
// others are about to mint; a substrate carrying only a project id is either a
// server-provisioned one bd must adopt rather than stamp, or a bootstrap that
// failed partway, and bd cannot tell those apart. The error carries both values
// so its caller can.
func RefuseIdentifiedSubstrate(prefix, projectID string) error {
	if prefix == "" && projectID == "" {
		return nil
	}
	return &issueops.AlreadyIdentifiedError{Prefix: prefix, ProjectID: projectID}
}
