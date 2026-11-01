package issueops

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestAsOfReadMapsAnyStoredRestrictionOutsideTheVocabularyToUnknown is finding
// 5 of the #6661 review. removed_restriction is a free VARCHAR(30) written by
// whatever produced the row, and AsOfReadInTx handed it back as an
// AsOfRestriction by a plain cast. The type's doc says the answer is one of four
// values and that AsOfRestrictionLive is never returned (Live is the absence of
// removed_at, not a stored value), yet "live" and any other string passed
// straight through, so a caller switching on the result saw a value the
// vocabulary does not contain and could not tell it from an answer.
//
// A stored value outside the four says nothing about why the row was removed,
// so it is reported as unknown: this store cannot say. The hyphenated spelling
// is in the table because it is what the migration header used to tell a writer
// to store.
func TestAsOfReadMapsAnyStoredRestrictionOutsideTheVocabularyToUnknown(t *testing.T) {
	const (
		issueID  = "bd-1"
		revision = int64(3)
		reason   = "retention window elapsed"
	)
	removedAt := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name   string
		stored any // the removed_restriction column; nil is SQL NULL
		want   AsOfRestriction
	}{
		{"retention", "gone_retention", AsOfRestrictionGoneRetention},
		{"erasure", "gone_erasure", AsOfRestrictionGoneErasure},
		{"reorganization", "gone_reorganization", AsOfRestrictionGoneReorganization},
		{"unknown", "unknown", AsOfRestrictionUnknown},
		{"empty string", "", AsOfRestrictionUnknown},
		{"NULL", nil, AsOfRestrictionUnknown},
		{"live is never a stored answer", "live", AsOfRestrictionUnknown},
		{"hyphenated spelling of a real value", "gone-retention", AsOfRestrictionUnknown},
		{"arbitrary text", "quarantined", AsOfRestrictionUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT 1 FROM issue_versions WHERE issue_id = ? AND revision = ?`)).
				WithArgs(issueID, revision).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			mock.ExpectQuery(`SELECT durable_state, removed_at, removed_reason, removed_restriction\s+FROM issue_versions`).
				WithArgs(issueID, revision).
				WillReturnRows(sqlmock.NewRows([]string{"durable_state", "removed_at", "removed_reason", "removed_restriction"}).
					AddRow([]byte("{}"), removedAt, reason, tt.stored))

			got, err := AsOfReadInTx(context.Background(), tx, "store", issueID,
				AsOfSelector{Address: AsOfVersionAddress(issueID, revision)})
			if err != nil {
				t.Fatalf("AsOfReadInTx: %v", err)
			}
			if !got.Refused {
				t.Fatalf("a version row with removed_at set must be refused, got %+v", got)
			}
			if got.Restriction != tt.want {
				t.Errorf("stored removed_restriction %#v: Restriction = %q, want %q", tt.stored, got.Restriction, tt.want)
			}
			if got.Reason != reason {
				t.Errorf("Reason = %q, want %q: the stored reason must survive the restriction being normalised", got.Reason, reason)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestListVersionsAgreesWithTheAsOfReadOnWhyAVersionWasRemoved is finding 3 of the
// #6661 review. AsOfReadInTx puts a stored removed_restriction through the four-value
// vocabulary: anything outside it, a NULL or empty column included, is unknown. The
// listing returned the column as stored, for every row, so bd versions and its JSON
// could name a restriction the as-of read never gives ("live", "quarantined", a
// hyphenated spelling), say nothing for a removed version the as-of read calls
// unknown, and attach a restriction to a version that was never removed. Both reads
// must answer the same question the same way: a removed version's restriction is one
// of the vocabulary's values, and a version that is not removed carries none.
//
// The as-of read is the oracle, run on the same stored value, so the two cannot drift
// apart again whichever one is changed.
func TestListVersionsAgreesWithTheAsOfReadOnWhyAVersionWasRemoved(t *testing.T) {
	const (
		issueID  = "bd-1"
		revision = int64(3)
		reason   = "retention window elapsed"
	)
	changeAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	removedAt := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name   string
		stored any // the removed_restriction column; nil is SQL NULL
	}{
		{"retention", "gone_retention"},
		{"erasure", "gone_erasure"},
		{"reorganization", "gone_reorganization"},
		{"unknown", "unknown"},
		{"empty string", ""},
		{"NULL", nil},
		{"live is never a stored answer", "live"},
		{"hyphenated spelling of a real value", "gone-retention"},
		{"arbitrary text", "quarantined"},
	} {
		for _, removed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/removed=%t", tt.name, removed), func(t *testing.T) {
				// The listing selects COALESCE(removed_restriction, ''), so NULL reaches it as "".
				listed, _ := tt.stored.(string)
				var removedAtValue any // nil is SQL NULL: a version that was never removed
				var storedReason string
				if removed {
					removedAtValue = removedAt
					storedReason = reason
				}

				_, mock, tx := beginMockTx(t)
				mock.ExpectQuery(`FROM issue_versions\s+WHERE issue_id = \?\s+ORDER BY revision DESC`).
					WithArgs(issueID).
					WillReturnRows(sqlmock.NewRows([]string{
						"issue_id", "revision", "epoch", "change_actor", "change_agent", "change_message",
						"change_at", "attribution_status", "removed_at", "removed_reason", "removed_restriction", "state_bytes",
					}).AddRow(issueID, revision, int64(1), "", "", "", changeAt, "", removedAtValue, storedReason, listed, int64(0)))
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT 1 FROM issue_versions WHERE issue_id = ? AND revision = ?`)).
					WithArgs(issueID, revision).
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(`SELECT durable_state, removed_at, removed_reason, removed_restriction\s+FROM issue_versions`).
					WithArgs(issueID, revision).
					WillReturnRows(sqlmock.NewRows([]string{"durable_state", "removed_at", "removed_reason", "removed_restriction"}).
						AddRow([]byte("{}"), removedAtValue, storedReason, tt.stored))

				versions, err := ListVersionsInTx(context.Background(), tx, issueID)
				if err != nil {
					t.Fatalf("ListVersionsInTx: %v", err)
				}
				asOf, err := AsOfReadInTx(context.Background(), tx, "store", issueID,
					AsOfSelector{Address: AsOfVersionAddress(issueID, revision)})
				if err != nil {
					t.Fatalf("AsOfReadInTx: %v", err)
				}
				if len(versions) != 1 {
					t.Fatalf("ListVersionsInTx returned %d versions, want 1", len(versions))
				}
				got := versions[0].RemovedRestriction

				if removed {
					if !asOf.Refused {
						t.Fatalf("the as-of read of a removed version must be refused, got %+v", asOf)
					}
					if got != string(asOf.Restriction) {
						t.Errorf("stored removed_restriction %#v: the listing says %q, the as-of read says %q; they must agree", tt.stored, got, asOf.Restriction)
					}
					return
				}
				if asOf.Refused {
					t.Fatalf("the as-of read of a version that was not removed must be served, got %+v", asOf)
				}
				if got != "" {
					t.Errorf("stored removed_restriction %#v on a version that was not removed: the listing says %q, want no restriction", tt.stored, got)
				}
			})
		}
	}
}
