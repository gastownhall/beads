package ownershiphandoffv2

import (
	"errors"
	"fmt"
)

// The typed refusal codes. Every non-zero exit from a verb carries exactly one
// of these, so a caller can branch on the reason without parsing prose.
const (
	// CodeInvalidRequest is a malformed or incomplete command line.
	CodeInvalidRequest = "invalid_request"
	// CodeUnsupportedScope is a scope this transfer does not handle: a socket
	// endpoint, a non-loopback host, a proxied target.
	CodeUnsupportedScope = "unsupported_scope"
	// CodeUnsupportedJournalVersion is a journal written by a bd whose phase
	// vocabulary differs from this one's. Refused in both directions: the
	// phase names overlap but do not mean the same thing.
	CodeUnsupportedJournalVersion = "unsupported_journal_version"
	// CodeIdentityConflict is a journal whose request names a different scope
	// than the one asked for. Journals are never merged.
	CodeIdentityConflict = "identity_conflict"
	// CodeLegacyUnreachable is a legacy endpoint that will not handshake, or
	// will not serve the named database, at prepare.
	CodeLegacyUnreachable = "legacy_unreachable"
	// CodeBDServerPresent is a live bd-managed server for this root. bd will
	// not transfer ownership to itself while it already holds it, and will not
	// mistake its own server for the caller's.
	CodeBDServerPresent = "bd_server_present"
	// CodeLegacyAlive is any evaluated liveness gate reporting the legacy owner
	// still running. No mutation has occurred.
	CodeLegacyAlive = "legacy_alive"
	// CodeDataDirLocked is Dolt's data-dir lock still held under the workspace.
	CodeDataDirLocked = "data_dir_locked"
	// CodeDataDirInvalid is a data dir that is neither a Dolt root nor
	// initializable as one.
	CodeDataDirInvalid = "data_dir_invalid"
	// CodeTargetLaunchFailed is a replacement server that did not start or did
	// not prove its identity. Rollback-eligible.
	CodeTargetLaunchFailed = "target_launch_failed"
	// CodeTargetIdentityChanged is a target whose captured identity no longer
	// matches the process now holding its pid. Refuses rather than signaling.
	CodeTargetIdentityChanged = "target_identity_changed"
	// CodeSentinelMismatch is the replacement serving different data than the
	// legacy server did. Rollback-eligible.
	CodeSentinelMismatch = "sentinel_mismatch"
	// CodeCommitRefused is the post-write assertion disagreeing with the write
	// set. The write set has been restored from the snapshot.
	CodeCommitRefused = "commit_refused"
	// CodeLegacyNotBack is rollback-finish's (i)–(iii): the caller's server is
	// not answering, or the restored artifacts have drifted.
	CodeLegacyNotBack = "legacy_not_back"
	// CodeLegacyIdentityUnproven is rollback-finish's (iv): something answers,
	// but bd cannot prove it is a stable process bound to this workspace and
	// distinct from the target and from bd's own server.
	CodeLegacyIdentityUnproven = "legacy_identity_unproven"
	// CodeJournalBusy is another verb holding the journal lock.
	CodeJournalBusy = "journal_busy"
	// CodePhaseOrder is a verb asked for out of order.
	CodePhaseOrder = "phase_order"
	// CodeAlreadyCommitted is a replay of a completed transfer. Exit 0.
	CodeAlreadyCommitted = "already_committed"
	// CodeJournalUnreadable is a journal that exists but cannot be read or
	// validated. Distinct from CodeUnsupportedJournalVersion, which is a
	// readable journal from a different vocabulary.
	CodeJournalUnreadable = "journal_unreadable"
)

// CodedError carries one of the codes above alongside the underlying cause.
type CodedError struct {
	Code string
	Err  error
}

func (e CodedError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e CodedError) Unwrap() error { return e.Err }

func coded(code string, err error) error { return CodedError{Code: code, Err: err} }

func codedf(code, format string, args ...any) error {
	return CodedError{Code: code, Err: fmt.Errorf(format, args...)}
}

// ErrorCode extracts the typed code from err, or "" when err carries none.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var c CodedError
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}
