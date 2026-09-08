// Package rowid derives deterministic, clone-stable primary-key ids for the
// auxiliary history tables (events, comments, issue_snapshots,
// compaction_snapshots).
//
// Background (bd-6dnrw.2, the aux-table sibling of gastownhall/beads#4259):
// migration 0037 converted these tables' BIGINT AUTO_INCREMENT primary keys to
// CHAR(36) by backfilling `uuid_id = UUID()` — a *different* random value on
// every clone. Because beads syncs by merging independent Dolt clones, two
// legacy clones that ran 0037 independently hold the same logical rows under
// two different primary keys, so their merges either refuse outright
// ("different primary keys in its common ancestor") or duplicate every
// pre-upgrade event and comment.
//
// Unlike dependency edges, these rows have no natural unique key, so the id is
// derived from the row's full content instead: rows that were identical in the
// clones' common ancestor are byte-identical on both sides, and hashing that
// content yields the same id everywhere. Exact-duplicate rows within one clone
// are disambiguated by an ordinal; across clones the *assignment* of ordinals
// to indistinguishable rows may permute, but the resulting id set is identical
// and the rows are interchangeable, so the merged result converges anyway.
//
// The derivation is consumed by the one-time upgrade backfill
// (rekeyAuxRowIDs in internal/storage/schema). Rows inserted after that
// backfill keep their app-minted random ids (issueops.NewEventID / UUIDv7
// comment ids; migration 0051 dropped the DB-side DEFAULT): a post-backfill
// row is created on exactly one clone and reaches the others by merge, so its
// id is random but consistent everywhere and needs no convergence.
package rowid

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Namespace is the fixed UUIDv5 namespace for beads aux-table row ids. It was
// generated once as uuid.NewSHA1(uuid.NameSpaceURL,
// "https://github.com/gastownhall/beads#row-id") and is hardcoded here forever:
// changing it (or the encoding below) would re-derive every backfilled id and
// re-introduce the cross-clone divergence this package exists to remove.
var Namespace = uuid.MustParse("92da14b5-20a8-5252-8eb2-3839e97b2156")

// sep separates the id key's components (table, ordinal, content digest), none
// of which can contain it: table is a SQL identifier, ordinal is decimal, and
// the digest is hex.
const sep = "\x1f"

// Digest returns the canonical content digest of one row, given its non-id
// column values in the table's frozen column order (see the rekey backfill for
// the per-table lists). Each field is encoded as "n" when NULL or
// "v<len>:<bytes>" otherwise; the encoding is a prefix code, so no two
// distinct field sequences collide even when values contain arbitrary bytes,
// and NULL is distinct from the empty string. The digest is the lowercase hex
// SHA-256 of that encoding.
func Digest(fields []sql.NullString) string {
	var b strings.Builder
	for _, f := range fields {
		if !f.Valid {
			b.WriteString("n")
			continue
		}
		b.WriteString("v")
		b.WriteString(strconv.Itoa(len(f.String)))
		b.WriteString(":")
		b.WriteString(f.String)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// New returns the deterministic CHAR(36) primary key for the row with the
// given content digest in table. ordinal disambiguates exact-duplicate rows
// (same digest) within one database: the n duplicates of a digest take
// ordinals 0..n-1. The same (table, ordinal, digest) yields the same id on
// every clone and in every process; it is a valid RFC-4122 v5 UUID.
func New(table string, ordinal int, digest string) string {
	return uuid.NewSHA1(Namespace, []byte(table+sep+strconv.Itoa(ordinal)+sep+digest)).String()
}
