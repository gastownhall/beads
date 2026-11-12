package fix

import (
	"context"
	"database/sql"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// IsUnverifiableDepTarget reports whether doctor must not judge a dependency
// target it cannot find in issues or wisps: an external: ref, or a bare ID that
// bd dep add stored unverified because its prefix names another repository.
// A prefix this database holds (issue_prefix, allowed_prefixes) is not another
// repository's, so a missing target under it is an orphan.
//
// The storage column is not the signal: migrations 0041, 0047 and 0035 park
// every missing target in depends_on_external, same-prefix ones included.
func IsUnverifiableDepTarget(sourceID, targetID, issuePrefix, allowedPrefixes string) bool {
	if strings.HasPrefix(targetID, "external:") {
		return true
	}
	// Same condition as bd dep add's pass-through (resolveUnresolvedDepTarget
	// in cmd/bd/dep.go), via the write side's classifier, so the two cannot
	// drift. A hyphenless ID is never stored unverified.
	if types.ExtractPrefix(sourceID) == "" || types.ExtractPrefix(targetID) == "" ||
		!issueops.IsExternalDepTarget(sourceID, targetID) {
		return false
	}
	// Match held prefixes as issueops.ValidateIssueIDPrefix does, so a
	// multi-hyphen prefix (hq-cv) is held; trim spaces and a trailing "-" as
	// the ID resolver does.
	for _, p := range append([]string{issuePrefix}, strings.Split(allowedPrefixes, ",")...) {
		p = strings.TrimSuffix(strings.TrimSpace(p), "-")
		if p != "" && strings.HasPrefix(targetID, p+"-") {
			return false
		}
	}
	return true
}

// LoadDepTargetPrefixes reads issue_prefix and allowed_prefixes from the
// database being checked; a missing key reads as "". Call it before opening a
// rows cursor on db: doctor's pools can be a single connection.
func LoadDepTargetPrefixes(ctx context.Context, db *sql.DB) (issuePrefix, allowedPrefixes string, err error) {
	rows, err := db.QueryContext(ctx,
		"SELECT `key`, COALESCE(`value`, '') FROM config WHERE `key` IN ('issue_prefix', 'allowed_prefixes')")
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return "", "", err
		}
		switch key {
		case "issue_prefix":
			issuePrefix = value
		case "allowed_prefixes":
			allowedPrefixes = value
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	return issuePrefix, allowedPrefixes, nil
}
