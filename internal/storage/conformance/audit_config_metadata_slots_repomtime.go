package conformance

import (
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
	t.Run("RepoMtimeClearKeyAsymmetry", func(t *testing.T) { testAuditRepoMtimeClearKeyAsymmetry(t, f) })
	t.Run("SlotGetNonStringValues", func(t *testing.T) { testAuditSlotGetNonStringValues(t, f) })
}
