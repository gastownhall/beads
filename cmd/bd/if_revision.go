package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// ifRevisionFlagHelp is the shared half of --if-revision's --help text across
// `bd update`, `bd close`, `bd assign` and `bd delete`: the value syntax, the
// one-id rule and the exit-code contract. Each verb appends what is specific
// to it (close has no other guards to compose with; update composes with
// --if-assignee/--if-status).
const ifRevisionFlagHelp = "Apply the write only if the issue's current revision equals this value (the decimal int64 from `bd show --json`'s \"revision\" field). One id only: refused before any write when combined with more than one issue id. A mismatch writes nothing and exits 13 (vs 1 for other failures)."

// parseIfRevisionFlag reads --if-revision with presence detected via
// Changed(), matching --if-assignee/--if-status's idiom, and parses it with
// types.ParseRevisionToken — the one encoding bd uses for a revision token on
// the wire, so a value copied from `bd show --json`'s "revision" field always
// parses and a typo is refused before any write rather than silently never
// matching.
func parseIfRevisionFlag(cmd *cobra.Command) (*int64, error) {
	if !cmd.Flags().Changed("if-revision") {
		return nil, nil
	}
	raw, _ := cmd.Flags().GetString("if-revision")
	v, err := types.ParseRevisionToken(strings.TrimSpace(raw))
	if err != nil {
		return nil, HandleErrorRespectJSON("invalid --if-revision %q: must be the decimal int64 revision from `bd show --json` (%v)", raw, err)
	}
	return &v, nil
}

// requireSingleIfRevisionID enforces A8's "one id only" rule: --if-revision
// names a single row's token, so more than one id is refused before any
// write rather than applying the one token to every id in the batch (T4.8).
func requireSingleIfRevisionID(ifRevision *int64, ids []string) error {
	if ifRevision == nil || len(ids) <= 1 {
		return nil
	}
	return HandleErrorRespectJSON("--if-revision guards one issue only; %d ids were given — send one guarded request per issue", len(ids))
}

// Machine body codes this CLI emits for a refused --if-revision write, shared
// verbatim with gascity's bdstore_conditional.go classifier, which switches on
// `code` rather than the exit code.
const (
	ifRevisionCodePreconditionFailed = "precondition_failed"
	ifRevisionCodeUnsupported        = "conditional_write_unsupported"
)

// classifyIfRevisionFailure maps err to the machine code and human reason
// gascity's bdstore_conditional.go classifier expects from a single-id write
// guarded by --if-revision: "precondition_failed" for a stale --if-revision,
// --if-assignee or --if-status guard (T4.5 requires all three to report
// through the one envelope when --if-revision is present), or
// "conditional_write_unsupported" for a backend that cannot honor
// ExpectedVersion at all. ok is false for any other failure, which the
// caller's own (unrelated) failure handling reports unchanged.
func classifyIfRevisionFailure(err error) (code, reason string, expected, current *int64, ok bool) {
	var vme *issueops.VersionMismatchError
	switch {
	case errors.As(err, &vme):
		return ifRevisionCodePreconditionFailed, "revision mismatch", &vme.Expected, &vme.Current, true
	case errors.Is(err, storage.ErrAssigneeMismatch):
		return ifRevisionCodePreconditionFailed, "assignee mismatch", nil, nil, true
	case errors.Is(err, storage.ErrStatusMismatch):
		return ifRevisionCodePreconditionFailed, "status mismatch", nil, nil, true
	}
	var unsupported *issueops.ErrUnsupported
	if errors.As(err, &unsupported) {
		return ifRevisionCodeUnsupported, "", nil, nil, true
	}
	return "", "", nil, nil, false
}

// ifRevisionFailureBody is the machine JSON this CLI attaches to a refused
// --if-revision write, decoded by gascity's bdConditionalErrorBody
// (bdstore_conditional.go). ExpectedRevision/CurrentRevision are pointers so
// an absent field (assignee/status guard, or unsupported) is distinguishable
// from a legitimate zero revision, and they marshal as JSON INTEGERS — never
// strings, unlike `bd show --json`'s "revision" — because gascity decodes them
// straight into *int64 and a string there makes its json.Decoder fail closed
// on the whole object (bdstore_conditional.go:134-139).
type ifRevisionFailureBody struct {
	Error            string `json:"error"`
	Code             string `json:"code"`
	ID               string `json:"id"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	CurrentRevision  *int64 `json:"current_revision,omitempty"`
}

// reportIfRevisionFailure reports a single-id write refused under an active
// --if-revision guard (alone or composed with --if-assignee/--if-status) and
// returns the exit error the CLI contract promises: ExitGuardMismatch (13)
// for a stale guard, 1 for a backend that cannot honor ExpectedVersion at
// all. action is the gerund for the human line ("updating", "closing",
// "assigning", "deleting"), matching this file's existing "Error <gerund>
// <id>: <reason>" convention. ok is false when err is not a guard outcome at
// all, in which case the caller's existing failure handling applies
// unchanged and nothing is printed here.
//
// In --json mode the JSON body is the LAST line on stderr, flat or under
// `data` per jsonEnvelopeEnabled — mirroring reportUpdateFailures — and the
// human "error" text always contains "precondition failed" so gascity's
// code-less fallback (bdstore_conditional.go:320-327) still matches if a
// caller ever loses the `code` field.
func reportIfRevisionFailure(action, id string, err error) (reportedErr error, ok bool) {
	code, reason, expected, current, ok := classifyIfRevisionFailure(err)
	if !ok {
		return nil, false
	}
	humanErr := "backend does not support --if-revision (conditional write unsupported)"
	if code == ifRevisionCodePreconditionFailed {
		humanErr = "precondition failed: " + reason
	}
	fmt.Fprintf(os.Stderr, "Error %s %s: %s\n", action, id, humanErr)

	if jsonOutput {
		body := ifRevisionFailureBody{Error: humanErr, Code: code, ID: id, ExpectedRevision: expected, CurrentRevision: current}
		var payload interface{}
		if jsonEnvelopeEnabled() {
			payload = map[string]interface{}{"schema_version": JSONSchemaVersion, "data": body}
		} else {
			payload = struct {
				ifRevisionFailureBody
				SchemaVersion int `json:"schema_version"`
			}{body, JSONSchemaVersion}
		}
		if data, merr := json.Marshal(payload); merr == nil {
			fmt.Fprintln(os.Stderr, string(data))
		}
	}

	if code == ifRevisionCodePreconditionFailed {
		return &exitError{Code: ExitGuardMismatch}, true
	}
	return &exitError{Code: 1}, true
}
