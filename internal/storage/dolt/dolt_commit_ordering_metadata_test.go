package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// Ordering contracts for the second GH#5752 migration wave: promote/demote
// (ephemeral_routing.go), MergeMetadata/SlotSet/SlotClear (slots.go), and the
// metadata CAS (metadata_cas.go) must publish their DOLT_ADD/DOLT_COMMIT
// strictly AFTER the driver-level COMMIT of the SQL transaction that applied
// the change — never inside it — preserving each site's table set, routing,
// guards, and commit message.

// serveMetadataRead serves the metadata plane's single-column read of the
// issue row: raw is the JSON bytes currently stored (nil for none).
func serveMetadataRead(c *seqConnector, raw []byte) {
	prev := c.rows
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "SELECT metadata FROM issues WHERE id = ?") {
			return &valRows{cols: []string{"metadata"}, vals: [][]driver.Value{{raw}}}, true
		}
		if strings.Contains(q, "SELECT COUNT(*) FROM issues WHERE id = ?") ||
			strings.Contains(q, "SELECT COUNT(*) FROM wisps WHERE id = ?") {
			// Existence checks (create-batch cache): no pre-existing row.
			return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
		}
		if prev != nil {
			return prev(q)
		}
		return nil, false
	}
}

func metadataMutationCases() []legacyMutationCase {
	return []legacyMutationCase{
		{
			name: "merge metadata",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
				serveMetadataRead(c, nil)
			},
			run: func(s *DoltStore) error {
				return s.MergeMetadata(context.Background(), "legacy-1", "slot.x", json.RawMessage(`"v"`), "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: merge metadata legacy-1.slot.x",
		},
		{
			name: "slot set",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
				serveMetadataRead(c, nil)
			},
			run: func(s *DoltStore) error {
				return s.SlotSet(context.Background(), "legacy-1", "slot.x", "v", "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: merge metadata legacy-1.slot.x",
		},
		{
			name: "slot clear",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", map[string]driver.Value{
					"metadata": []byte(`{"slot.x":"v"}`),
				})
				serveMetadataRead(c, []byte(`{"slot.x":"v"}`))
			},
			run: func(s *DoltStore) error {
				return s.SlotClear(context.Background(), "legacy-1", "slot.x", "alice")
			},
			tables: []string{"issues", "events"},
			msg:    "bd: clear metadata legacy-1.slot.x",
		},
		{
			name: "compare-and-set metadata swap",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", map[string]driver.Value{
					"metadata": []byte(`{"caskey":"old"}`),
				})
				serveMetadataRead(c, []byte(`{"caskey":"old"}`))
			},
			run: func(s *DoltStore) error {
				cas, err := s.MetadataCAS()
				if err != nil {
					return err
				}
				expected := json.RawMessage(`"old"`)
				value := json.RawMessage(`"new"`)
				result, err := cas.CompareAndSetKey(context.Background(), issueops.CompareAndSetKeyRequest{
					Actor:    "alice",
					IssueID:  "legacy-1",
					Key:      "caskey",
					Expected: &expected,
					Value:    &value,
				})
				if err != nil {
					return err
				}
				if !result.Swapped {
					return errSwappedExpectedTrue
				}
				return nil
			},
			tables: []string{"issues", "events"},
			msg:    "bd: compare-and-set metadata legacy-1.caskey",
		},
		{
			name: "promote from ephemeral",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveMetadataRead(c, nil)
				c.rows = func(q string) (driver.Rows, bool) {
					// The wisp probe must report the ephemeral plane...
					if strings.Contains(q, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1") {
						return &valRows{cols: []string{"exists"}, vals: [][]driver.Value{{int64(1)}}}, true
					}
					// ...and GetIssueInTx's wisp fallback serves the wisp row
					// (the issues read falls through empty).
					if strings.Contains(q, "FROM wisps") && strings.Contains(q, "LEFT JOIN leases") && strings.Contains(q, "WHERE id = ?") {
						return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
					}
					if strings.Contains(q, "SELECT COUNT(*) FROM issues WHERE id = ?") ||
						strings.Contains(q, "SELECT COUNT(*) FROM wisps WHERE id = ?") {
						return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
					}
					return nil, false
				}
			},
			run: func(s *DoltStore) error {
				return s.PromoteFromEphemeral(context.Background(), "legacy-1", "alice")
			},
			tables: []string{"issues", "labels", "dependencies", "events", "comments", "provenance_events"},
			msg:    "bd: promote legacy-1",
		},
		{
			name: "demote to wisp",
			wire: func(c *seqConnector) {
				legacyIssueEvents(c)
				serveLegacyIssueRow(c, "legacy-1", nil)
			},
			run: func(s *DoltStore) error {
				return s.DemoteToWisp(context.Background(), "legacy-1", map[string]interface{}{"no_history": true}, "alice")
			},
			tables: []string{"issues", "labels", "dependencies", "events", "comments", "provenance_events"},
			msg:    "bd: demote legacy-1 to wisp",
		},
	}
}

var errSwappedExpectedTrue = &swapVerdictError{}

type swapVerdictError struct{}

func (*swapVerdictError) Error() string { return "compare-and-set swap verdict = false, want true" }

func TestMetadataAndPlaneMutationsDoltCommitRunsAfterTxCommit(t *testing.T) {
	for _, tc := range metadataMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			rec := &seqRecorder{}
			connector := &seqConnector{rec: rec}
			tc.wire(connector)
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()

			s := &DoltStore{db: db}
			if err := tc.run(s); err != nil {
				t.Fatalf("%s: %v; events: %v", tc.name, err, rec.snapshot())
			}

			assertPostTxPublication(t, rec.snapshot(), tc.tables, tc.msg)
		})
	}
}
