package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// runDoltCommitProxiedServer is the proxied dual of `bd dolt commit`: the
// explicit commit point that discharges dolt.auto-commit=batch/off on this
// route (GH#4995). Proxied mode returns from the root pre-run before
// newDoltStore ever runs, so getStore() is nil here and the UOW provider is the
// only handle on the server — without this dual, batch/off on a proxied
// checkout meant "never commit" and writes piled up in the working set forever.
//
// It deliberately does not open a unit of work: doltServerTx.Commit blanks its
// message under a deferred context (that is the deferral this command exists to
// discharge), so routing the flush through it would make the flush itself a
// no-op. It runs DOLT_COMMIT('-Am') on a pinned maintenance connection instead,
// which is the uow-side equivalent of DoltStorage.CommitAll: "everything in the
// working set", including out-of-band and config-table dirt, attributed to the
// same actor with '--author'.
func runDoltCommitProxiedServer(ctx context.Context, message string) (bool, error) {
	committed := false
	// runProxiedNonTx, not an open-coded provider assertion: it is the shared
	// maintenance escape hatch (backup, compact, clean-databases) and it reports
	// provider failures through HandleErrorRespectJSON, so `bd --json dolt
	// commit` keeps emitting the JSON error envelope the rest of the proxied
	// surface emits.
	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		// The same gate doltServerTx.Commit uses, and for the same reason: an
		// empty DOLT_COMMIT is rejected server-side with "nothing to commit"
		// and floods the server log.
		pending, err := issueops.HasPendingChanges(ctx, conn)
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?);", message, proxiedCommitAuthor()); err != nil {
			if isDoltNothingToCommit(err) {
				return nil
			}
			return err
		}
		committed = true
		return nil
	})
	return committed, err
}

// proxiedCommitAuthor is the '--author' identity for the proxied flush, the
// counterpart of DoltStorage.commitAuthorString on the direct route. Without it
// the commit is attributed to the SQL session user, and under batch mode this is
// the ONE commit that survives a whole batch of writes — so on a multi-agent
// deployment `dolt log` could not say whose batch was flushed.
//
// The actor half is the same getActor() the default commit message names; the
// address half comes from git, as it does for the committer identity the store
// resolves for its own commits.
func proxiedCommitAuthor() string {
	name, email := proxiedServerCommitter()
	if a := strings.TrimSpace(getActor()); a != "" {
		name = a
	}
	return renderDoltCommitAuthor(name, email)
}

// renderDoltCommitAuthor formats a Dolt '--author' argument. Dolt parses the
// git "Name <email>" grammar and rejects anything else, so the angle brackets
// and newlines an actor name could carry are stripped rather than passed
// through to fail the flush.
func renderDoltCommitAuthor(name, email string) string {
	clean := strings.NewReplacer("<", "", ">", "", "\n", " ", "\r", " ")
	name = strings.TrimSpace(clean.Replace(name))
	email = strings.TrimSpace(clean.Replace(email))
	if name == "" {
		name = "beads"
	}
	if email == "" {
		email = "beads@localhost"
	}
	return fmt.Sprintf("%s <%s>", name, email)
}
