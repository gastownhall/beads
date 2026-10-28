//go:build cgo

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The IDRetention contract on the served leg: the retaining sweep travels as
// `retain_ids` behind issues.sweep.retainIds, and a create of a retired id
// comes back as the 410 idempotency_expired this client rebuilds into the
// typed *issueops.IdempotencyExpiredError — on createIssue and on an
// applyBatch create item alike.
//
// Two cases park on rows that predate this contract, because the case needs a
// member the wire deliberately does not publish: the batch-create case names
// its items' ids (BatchCreateItem has no id member), and the mint-collision
// case pins created_at (createIssue withholds it). The in-transaction case is
// waived for this leg by name (no transaction spans requests), and so are the
// two cases that drive the upsert-shaped single create (UpsertCreate), since
// the wire has none (createIssue is create-only), and the two import cases:
// the wire publishes no import operation, so the client hands out no Importer.

func newServedIDRetentionFixture(t *testing.T, prefix string) conformance.IDRetentionFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	sweep := newServedSweeperFixtureOnEnv(t, env)
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	reader, err := env.subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	applier, err := env.subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	creator, err := env.subject.BatchCreator()
	if err != nil {
		t.Fatalf("BatchCreator(): %v", err)
	}
	workspaceConfig, err := env.subject.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	return conformance.IDRetentionFixture{
		IssuePrefix:  env.prefix,
		Sweep:        sweep,
		Lifecycle:    lifecycle,
		Reader:       reader,
		BatchApplier: applier,
		BatchCreator: creator,
		// types.retained is written through the wire's config surface, so
		// the retained-type case also proves the server's sweep reads the
		// setting a client stored.
		WorkspaceConfig: workspaceConfig,
		CommittedRetired: func(ctx context.Context, id string) (bool, error) {
			var count int
			err := env.queryScalar(ctx, "SELECT COUNT(*) FROM id_tombstones AS OF 'HEAD' WHERE id = ?", []any{id}, &count)
			return count > 0, err
		},
	}
}

func TestServedIDRetentionAPlainSweepLeavesTheIDReusable(t *testing.T) {
	conformance.RunIDRetentionAPlainSweepLeavesTheIDReusable(t, t.Context(), newServedIDRetentionFixture(t, "hidr0"))
}

func TestServedIDRetentionARetainingSweepRetiresEverySweptID(t *testing.T) {
	conformance.RunIDRetentionARetainingSweepRetiresEverySweptID(t, t.Context(), newServedIDRetentionFixture(t, "hidr1"))
}

func TestServedIDRetentionRefusesARecreateThroughCreate(t *testing.T) {
	conformance.RunIDRetentionRefusesARecreateThroughCreate(t, t.Context(), newServedIDRetentionFixture(t, "hidr2"))
}

func TestServedIDRetentionRefusesARecreateOnTheOtherPlane(t *testing.T) {
	conformance.RunIDRetentionRefusesARecreateOnTheOtherPlane(t, t.Context(), newServedIDRetentionFixture(t, "hidr3"))
}

func TestServedIDRetentionRefusesThroughABatchApply(t *testing.T) {
	conformance.RunIDRetentionRefusesThroughABatchApply(t, t.Context(), newServedIDRetentionFixture(t, "hidr4"))
}

func TestServedIDRetentionRefusesThroughABatchCreate(t *testing.T) {
	skipKnownDivergence(t, "W-BatchCreateItem.Issue", bulkParkBead, parkBatchExplicitID)
	conformance.RunIDRetentionRefusesThroughABatchCreate(t, t.Context(), newServedIDRetentionFixture(t, "hidr5"))
}

func TestServedIDRetentionNeverMintsARetiredID(t *testing.T) {
	skipKnownDivergence(t, "W-CreateRequest.Issue", createParkBead,
		"the case forces a generated-id collision by pinning Issue.CreatedAt, the creation stamp createIssue deliberately withholds; "+
			"the client refuses it before any request is sent (TestCreateRefusesEveryMemberTheWireExcludes). The generator skip it "+
			"proves is server-side and runs on every store leg.")
	conformance.RunIDRetentionNeverMintsARetiredID(t, t.Context(), newServedIDRetentionFixture(t, "hidr8"))
}

func TestServedIDRetentionNeverMintsARetiredChildID(t *testing.T) {
	conformance.RunIDRetentionNeverMintsARetiredChildID(t, t.Context(), newServedIDRetentionFixture(t, "hidrg"))
}

func TestServedIDRetentionDryRunRetiresNothing(t *testing.T) {
	conformance.RunIDRetentionDryRunRetiresNothing(t, t.Context(), newServedIDRetentionFixture(t, "hidr9"))
}

func TestServedIDRetentionRetiresOnlyWhatItSwept(t *testing.T) {
	conformance.RunIDRetentionRetiresOnlyWhatItSwept(t, t.Context(), newServedIDRetentionFixture(t, "hidra"))
}

func TestServedIDRetentionARetainingDurableSweepRecordsExactlyOneHistoryEntry(t *testing.T) {
	conformance.RunIDRetentionARetainingDurableSweepRecordsExactlyOneHistoryEntry(t, t.Context(), newServedIDRetentionFixture(t, "hidrb"))
}

func TestServedIDRetentionARacingRecreateNeverLands(t *testing.T) {
	conformance.RunIDRetentionARacingRecreateNeverLands(t, t.Context(), newServedIDRetentionFixture(t, "hidrc"))
}

func TestServedIDRetentionRefusesANoHistoryRecreateThroughEveryDoor(t *testing.T) {
	conformance.RunIDRetentionRefusesANoHistoryRecreateThroughEveryDoor(t, t.Context(), newServedIDRetentionFixture(t, "hidre"))
}

func TestServedIDRetentionAWispsPlaneRetainingSweepRetiresItsRows(t *testing.T) {
	conformance.RunIDRetentionAWispsPlaneRetainingSweepRetiresItsRows(t, t.Context(), newServedIDRetentionFixture(t, "hidrf"))
}

func TestServedIDRetentionAPlainSweepSkipsARetainedType(t *testing.T) {
	conformance.RunIDRetentionAPlainSweepSkipsARetainedType(t, t.Context(), newServedIDRetentionFixture(t, "hidrg"))
}

func TestServedIDRetentionARetainedTypeReadErrorFailsTheSweep(t *testing.T) {
	conformance.RunIDRetentionARetainedTypeReadErrorFailsTheSweep(t, t.Context(), newServedIDRetentionFixture(t, "hidrh"))
}

func TestServedIDRetentionARetainedTypeIsHeldBackBeforeTheProtectionsAndTheLimit(t *testing.T) {
	conformance.RunIDRetentionARetainedTypeIsHeldBackBeforeTheProtectionsAndTheLimit(t, t.Context(), newServedIDRetentionFixture(t, "hidri"))
}
