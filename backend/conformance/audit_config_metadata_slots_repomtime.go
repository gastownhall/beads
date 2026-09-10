package conformance

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Audit cases for the "config-metadata-slots-repomtime" slice. Each case pins a
// behavior of the embedded-Dolt reference that the existing conformance suite
// leaves unexercised: config key-column collation, the normalized custom-status/
// custom-type tables that SetConfig("status.custom"/"types.custom") sync (and that
// DeleteConfig deliberately does not), the SetConfig validation/rollback contract
// for status.custom, the verbatim-key vs abs-normalized-key asymmetry between
// Set/Get and Clear RepoMtime, and SlotGet's json.Marshal fall-through branch for
// non-string metadata values. Validated against embedded-Dolt (the oracle).

// auditDetailedPairs renders detailed custom statuses as a sorted "name:category"
// set, for order-independent comparison.
func auditDetailedPairs(ss []types.CustomStatus) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name + ":" + string(s.Category)
	}
	sort.Strings(out)
	return out
}

// auditFoldCount counts config entries whose key case-insensitively equals want.
func auditFoldCount(all map[string]string, want string) int {
	n := 0
	for k := range all {
		if strings.EqualFold(k, want) {
			n++
		}
	}
	return n
}

// testAuditConfigKeyCaseSensitive pins the reference's config key-column collation.
// The finding predicted Dolt's VARCHAR primary key would be case-insensitive, but
// the embedded-Dolt oracle treats "MyKey" and "mykey" as DISTINCT keys: each holds
// its own value and GetAllConfig carries both rows. SQLite's BINARY collation matches
// that behavior.
func testAuditConfigKeyCaseSensitive(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "MyKey", "a"))
	must(t, s.SetConfig(c, "mykey", "b"))

	if v, _ := s.GetConfig(c, "MyKey"); v != "a" {
		t.Errorf(`GetConfig("MyKey") = %q, want "a" (case-sensitive: distinct key)`, v)
	}
	if v, _ := s.GetConfig(c, "mykey"); v != "b" {
		t.Errorf(`GetConfig("mykey") = %q, want "b"`, v)
	}
	all, err := s.GetAllConfig(c)
	must(t, err)
	if n := auditFoldCount(all, "mykey"); n != 2 {
		t.Errorf("GetAllConfig has %d entries case-folding to \"mykey\", want 2 (distinct rows)", n)
	}
}

// testAuditCustomStatusesOrder pins that SetConfig("status.custom", ...) syncs the
// normalized custom_statuses table and GetCustomStatuses reads it back ORDER BY
// name — alphabetical, independent of the config-string order. GetCustomStatusesDetailed
// carries each status's category in that same alphabetical order.
func testAuditCustomStatusesOrder(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "status.custom", "zebra:wip,alpha:done"))

	names, err := s.GetCustomStatuses(c)
	must(t, err)
	if !slices.Equal(names, []string{"alpha", "zebra"}) {
		t.Errorf("GetCustomStatuses = %v, want [alpha zebra] (ORDER BY name)", names)
	}

	detailed, err := s.GetCustomStatusesDetailed(c)
	must(t, err)
	if got := auditDetailedPairs(detailed); !slices.Equal(got, []string{"alpha:done", "zebra:wip"}) {
		t.Errorf("GetCustomStatusesDetailed = %v, want [alpha:done zebra:wip]", got)
	}
}

// testAuditSetConfigInvalidStatusRollsBack pins the validation contract: SetConfig
// routes status.custom through SyncCustomStatusesTable, which parses+validates the
// value and returns an error for an invalid one, rolling back the whole write tx.
// So SetConfig returns an error AND nothing is stored (GetConfig empty). A backend
// that skips the sync would silently store the invalid value and return nil.
//
// NOT DOMINATED by RunWorkspaceConfigRefusesAnUnparseableCustomStatus, however
// alike the two read. The role refuses in workapi.ValidateSettingWrite, which
// parses the value BEFORE calling the store at all ("Checking here rather than
// leaving it to SyncCustomStatusesTable is what makes the refusal a validation
// error rather than a storage failure"). The store's own in-transaction refusal
// is therefore reachable only from this seam: make the store swallow the sync
// error and the whole workspaceconfig contract stays green while this case goes
// red (measured with scripts/mutation-equivalence.sh). The two also drive
// different parser branches — a name-shaped refusal here, a built-in collision
// there.
func testAuditSetConfigInvalidStatusRollsBack(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	// "Bad Name": uppercase + space fails the status-name regexp in ParseCustomStatusConfig.
	if err := s.SetConfig(c, "status.custom", "Bad Name"); err == nil {
		t.Fatal(`SetConfig("status.custom","Bad Name") = nil, want a validation error`)
	}
	v, err := s.GetConfig(c, "status.custom")
	must(t, err)
	if v != "" {
		t.Errorf(`GetConfig("status.custom") = %q after failed set, want "" (rolled back)`, v)
	}
}

// testAuditCustomTypesOrder pins that SetConfig("types.custom", ...) syncs the
// normalized custom_types table and GetCustomTypes reads it back ORDER BY name —
// alphabetical, independent of the JSON-array order in the config string. (The
// YAML overlay-union adds nothing here: in-process store tests have no
// .beads/config.yaml.)
func testAuditCustomTypesOrder(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "types.custom", `["zebra","alpha"]`))

	got, err := s.GetCustomTypes(c)
	must(t, err)
	if !slices.Equal(got, []string{"alpha", "zebra"}) {
		t.Errorf("GetCustomTypes = %v, want [alpha zebra] (ORDER BY name)", got)
	}
}

// testAuditDeleteConfigLeavesNormalizedTable pins that DeleteConfig removes only
// the config row and never touches the normalized custom_statuses table that an
// earlier SetConfig populated. So after deleting status.custom the reference still
// reports the custom statuses (stale table). A backend that never synced the table
// would instead report empty.
//
// RunWorkspaceConfigUnsetLeavesTheProjectionBehind pins the same bd-yby99.33
// clause on three legs and reads the projection table raw rather than through
// GetCustomStatuses, so on promises alone this case is dominated. It survives
// because it is the only call to DoltStorage.DeleteConfig anywhere in RunAll,
// the gate an out-of-tree backend proves itself with.
func testAuditDeleteConfigLeavesNormalizedTable(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "status.custom", "alpha:wip"))
	if names, _ := s.GetCustomStatuses(c); !slices.Equal(names, []string{"alpha"}) {
		t.Fatalf("precondition: GetCustomStatuses = %v, want [alpha]", names)
	}

	must(t, s.DeleteConfig(c, "status.custom"))

	if v, _ := s.GetConfig(c, "status.custom"); v != "" {
		t.Errorf("GetConfig(status.custom) = %q after delete, want \"\"", v)
	}
	names, err := s.GetCustomStatuses(c)
	must(t, err)
	if !slices.Equal(names, []string{"alpha"}) {
		t.Errorf("GetCustomStatuses = %v after DeleteConfig, want [alpha] (normalized table still populated)", names)
	}
}

// testAuditUnconfiguredVocabulary pins the success path a fresh workspace takes
// on its very first list-shaped command: with no custom vocabulary configured,
// GetCustomStatuses, GetCustomStatusesDetailed and GetCustomTypes answer empty
// with a nil error, and GetInfraTypes answers the default infra set.
//
// Every existing vocabulary case configures the vocabulary first, so the
// unconfigured path had no owning proof and GetInfraTypes had none at all.
// The consumption is a hard failure edge: internal/workapi/list.go LoadListConfig
// loads all four up front and wraps any error, so a backend that answers a scan
// error or a nil-map surprise for an unconfigured workspace bricks `bd list`,
// `bd ready` and every other list-shaped command rather than degrading.
//
// The infra set is spelled out rather than compared against the production
// constant: this suite is the contract, and reading the answer from the same
// place the implementation reads it would assert nothing.
func testAuditUnconfiguredVocabulary(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	names, err := s.GetCustomStatuses(c)
	must(t, err)
	if len(names) != 0 {
		t.Errorf("GetCustomStatuses on an unconfigured workspace = %v, want empty", names)
	}

	detailed, err := s.GetCustomStatusesDetailed(c)
	must(t, err)
	if len(detailed) != 0 {
		t.Errorf("GetCustomStatusesDetailed on an unconfigured workspace = %v, want empty", auditDetailedPairs(detailed))
	}

	custom, err := s.GetCustomTypes(c)
	must(t, err)
	if len(custom) != 0 {
		t.Errorf("GetCustomTypes on an unconfigured workspace = %v, want empty", custom)
	}

	infra := s.GetInfraTypes(c)
	if !maps.Equal(infra, map[string]bool{"agent": true, "role": true, "message": true}) {
		t.Errorf("GetInfraTypes on an unconfigured workspace = %v, want the default agent/role/message set", infra)
	}
}

// testAuditConfiguredInfraTypes pins the other half: a configured types.infra
// key replaces the default set outright, and the answer is exactly the
// configured names. Infra types decide which issue types route to the wisps
// table instead of the versioned issues table, so a backend that ignores the
// key silently versions rows the workspace asked to keep ephemeral.
//
// Subject: SetConfig. A backend whose allowlist refuses it does not run this
// case. The assertion is the value, not the cache: invalidation is a per-store
// concern this suite declines to pin, which is why the read happens on a store
// that has not read the key before.
func testAuditConfiguredInfraTypes(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "types.infra", "gate,probe"))

	infra := s.GetInfraTypes(c)
	if !maps.Equal(infra, map[string]bool{"gate": true, "probe": true}) {
		t.Errorf("GetInfraTypes after types.infra=gate,probe = %v, want exactly gate/probe", infra)
	}
}

// testAuditRepoMtimeClearKeyAsymmetry pins the verbatim-key vs abs-normalized-key
// asymmetry: SetRepoMtime/GetRepoMtime use the path argument verbatim, but
// ClearRepoMtime first resolves it to an absolute path before the DELETE. So
// clearing with the same relative path used to set matches a different key and is a
// silent no-op — the entry survives. Every backend must reproduce this identically.
func testAuditRepoMtimeClearKeyAsymmetry(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	const rel = "relative/repo"
	must(t, s.SetRepoMtime(c, rel, rel+"/j", 99))
	if v, err := s.GetRepoMtime(c, rel); err != nil || v != 99 {
		t.Fatalf("after set = (%d,%v), want (99,nil) (verbatim key)", v, err)
	}

	// Clear resolves rel to an absolute path, whose DELETE matches nothing.
	must(t, s.ClearRepoMtime(c, rel))
	if v, err := s.GetRepoMtime(c, rel); err != nil || v != 99 {
		t.Fatalf("after clear = (%d,%v), want (99,nil) — Clear abs-normalized and deleted nothing", v, err)
	}
}

// testAuditSlotGetNonStringValues pins SlotGet's json.Marshal fall-through: only
// string metadata values return raw; every other JSON type is re-marshaled to text.
// Numbers/bools/arrays render as JSON scalars/lists and object keys come back sorted
// (Go's json.Marshal of map[string]interface{}). Reachable only via seeded metadata,
// since SlotSet only ever stores strings.
func testAuditSlotGetNonStringValues(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	meta := `{"n":5,"b":true,"arr":[1,2],"obj":{"y":2,"x":1},"s":"str"}`
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-slotmeta", Title: "T", Metadata: []byte(meta)}), "a"))

	cases := []struct{ key, want string }{
		{"n", "5"},
		{"b", "true"},
		{"arr", "[1,2]"},
		{"obj", `{"x":1,"y":2}`}, // Go json.Marshal sorts object keys.
		{"s", "str"},             // string branch: raw, no quotes.
	}
	for _, tc := range cases {
		v, err := s.SlotGet(c, "test-slotmeta", tc.key)
		must(t, err)
		if v != tc.want {
			t.Errorf("SlotGet(%q) = %q, want %q", tc.key, v, tc.want)
		}
	}
}

// RunAudit_config_metadata_slots_repomtime runs the slice's audit cases. The Dolt
// reference passes all of them; SQL backends run them once the surface is wired.
func RunAudit_config_metadata_slots_repomtime(t *testing.T, f Factory) {
	t.Helper()
	t.Run("ConfigKeyCaseSensitive", func(t *testing.T) { testAuditConfigKeyCaseSensitive(t, f) })
	t.Run("CustomStatusesOrder", func(t *testing.T) { testAuditCustomStatusesOrder(t, f) })
	t.Run("SetConfigInvalidStatusRollsBack", func(t *testing.T) { testAuditSetConfigInvalidStatusRollsBack(t, f) })
	t.Run("CustomTypesOrder", func(t *testing.T) { testAuditCustomTypesOrder(t, f) })
	t.Run("DeleteConfigLeavesNormalizedTable", func(t *testing.T) { testAuditDeleteConfigLeavesNormalizedTable(t, f) })
	t.Run("UnconfiguredVocabulary", func(t *testing.T) { testAuditUnconfiguredVocabulary(t, f) })
	t.Run("ConfiguredInfraTypes", func(t *testing.T) { testAuditConfiguredInfraTypes(t, f) })
	t.Run("RepoMtimeClearKeyAsymmetry", func(t *testing.T) { testAuditRepoMtimeClearKeyAsymmetry(t, f) })
	t.Run("SlotGetNonStringValues", func(t *testing.T) { testAuditSlotGetNonStringValues(t, f) })
}
