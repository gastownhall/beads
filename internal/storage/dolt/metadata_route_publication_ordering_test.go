package dolt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestMetadataAndRoutingWritesPublishAfterSQLCommit pins GH#5752's ordering
// contract over the metadata and plane-routing writes: MergeMetadata (and
// SlotSet built on it), SlotClear, the metadata compare-and-set role,
// PromoteFromEphemeral, and demotion — direct, through UpdateIssue's routing,
// and through UpdateIssueChecked's guarded routing — each run their
// DOLT_ADD/DOLT_COMMIT only after the SQL COMMIT, outside any open
// transaction, staging the table set and message they always used.
func TestMetadataAndRoutingWritesPublishAfterSQLCommit(t *testing.T) {
	issueEvents := []string{"issues", "events"}
	cases := []struct {
		name     string
		metadata string
		wisp     bool
		run      func(context.Context, *DoltStore) error
		mutation func(pubEvent) bool
		tables   []string
		message  string
	}{
		{
			name: "MergeMetadata",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.MergeMetadata(ctx, "pub-1", "gc.k", json.RawMessage(`"v"`), "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: merge metadata pub-1.gc.k",
		},
		{
			name: "SlotSet",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.SlotSet(ctx, "pub-1", "slot", "v", "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: merge metadata pub-1.slot",
		},
		{
			name:     "SlotClear",
			metadata: `{"slot":"v"}`,
			run: func(ctx context.Context, s *DoltStore) error {
				return s.SlotClear(ctx, "pub-1", "slot", "alice")
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: clear metadata pub-1.slot",
		},
		{
			name: "MetadataCompareAndSetKey",
			run: func(ctx context.Context, s *DoltStore) error {
				cas, err := s.MetadataCAS()
				if err != nil {
					return err
				}
				value := json.RawMessage(`"holder"`)
				res, err := cas.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
					Actor: "alice", IssueID: "pub-1", Key: "gc.lease", Value: &value,
				})
				if err == nil && !res.Swapped {
					t.Errorf("CompareAndSetKey did not swap: %+v", res)
				}
				return err
			},
			mutation: writesTo("issues"),
			tables:   issueEvents,
			message:  "bd: compare-and-set metadata pub-1.gc.lease",
		},
		{
			name: "PromoteFromEphemeral",
			wisp: true,
			run: func(ctx context.Context, s *DoltStore) error {
				return s.PromoteFromEphemeral(ctx, "pub-1", "alice")
			},
			mutation: writesTo("issues"),
			tables:   permanentIssueAuxTables,
			message:  "bd: promote pub-1",
		},
		{
			name: "DemoteToWisp",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.DemoteToWisp(ctx, "pub-1", map[string]interface{}{"wisp": true}, "alice")
			},
			mutation: writesTo("issues"),
			tables:   permanentIssueAuxTables,
			message:  "bd: demote pub-1 to wisp",
		},
		{
			name: "UpdateIssueRoutesToDemotion",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UpdateIssue(ctx, "pub-1", map[string]interface{}{"no_history": true}, "alice")
			},
			mutation: writesTo("issues"),
			tables:   permanentIssueAuxTables,
			message:  "bd: demote pub-1 to wisp",
		},
		{
			name: "UpdateIssueCheckedRoutesToDemotion",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UpdateIssueChecked(ctx, "pub-1", map[string]interface{}{"wisp": true}, "alice", storage.UpdateIssueOptions{})
			},
			mutation: writesTo("issues"),
			tables:   permanentIssueAuxTables,
			message:  "bd: demote pub-1 to wisp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newPublicationOrderDriver()
			d.issueMetadata = tc.metadata
			d.wispExists = tc.wisp
			s := d.store()
			t.Cleanup(func() { _ = s.db.Close() })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := tc.run(ctx, s); err != nil {
				t.Fatalf("%s: %v; events:%s", tc.name, err, formatPubEvents(d.snapshot()))
			}
			pub := requirePublishedAfterCommit(t, tc.name, d.snapshot(), tc.mutation)
			requirePublication(t, tc.name, pub, tc.tables, tc.message)
		})
	}
}
