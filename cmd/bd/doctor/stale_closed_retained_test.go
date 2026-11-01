package doctor

import "testing"

// TestCountStaleCleanableHoldsBackRetainedTypes pins the Stale Closed Issues
// check's count: rows of a type types.retained names are counted apart, since
// the check's own fix never deletes them; a check that counted them as
// cleanable would warn forever about rows nothing it suggests removes.
func TestCountStaleCleanableHoldsBackRetainedTypes(t *testing.T) {
	byType := map[string]int{"task": 3, "op": 2}
	if cleanable, retained := countStaleCleanable(byType, "op"); cleanable != 3 || retained != 2 {
		t.Errorf("countStaleCleanable(op retained) = %d, %d; want 3, 2", cleanable, retained)
	}
	if cleanable, retained := countStaleCleanable(byType, ""); cleanable != 5 || retained != 0 {
		t.Errorf("countStaleCleanable(unset) = %d, %d; want 5, 0", cleanable, retained)
	}
	if cleanable, retained := countStaleCleanable(map[string]int{"op": 2}, "op"); cleanable != 0 || retained != 2 {
		t.Errorf("countStaleCleanable(only retained) = %d, %d; want 0, 2", cleanable, retained)
	}
}

// TestStaleClosedCheckReportsRetainedRows pins what the check says once the
// count is split: nothing cleanable but retained rows is OK and says why they
// stay; cleanable rows warn and say how many retained ones the fix will keep.
func TestStaleClosedCheckReportsRetainedRows(t *testing.T) {
	ok := staleClosedCheckResult(0, 2, 30)
	if ok.Status != StatusOK || ok.Message != "No stale closed issues" || ok.Detail == "" {
		t.Errorf("staleClosedCheckResult(0, 2) = %+v; want OK with a detail naming the retained rows", ok)
	}
	warn := staleClosedCheckResult(3, 2, 30)
	if warn.Status != StatusWarning || warn.Message != "3 closed issue(s) older than 30 days" {
		t.Errorf("staleClosedCheckResult(3, 2) = %+v; want a warning counting only the cleanable rows", warn)
	}
	if none := staleClosedCheckResult(0, 0, 30); none.Status != StatusOK || none.Detail != "" {
		t.Errorf("staleClosedCheckResult(0, 0) = %+v; want a plain OK", none)
	}
}
