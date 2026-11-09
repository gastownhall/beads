package versioncontrolops

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func mappingStrings(ms []storage.ChildRenumberMapping) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		tag := "d"
		if m.Root {
			tag = "r"
		}
		out[i] = tag + ":" + m.OldID + "->" + m.NewID
	}
	return out
}

func wantMappings(t *testing.T, got []storage.ChildRenumberMapping, want []storage.ChildRenumberMapping) {
	t.Helper()
	gotStrs := mappingStrings(got)
	wantStrs := mappingStrings(want)
	if len(gotStrs) != len(wantStrs) {
		t.Fatalf("mappings = %v, want %v", gotStrs, wantStrs)
	}
	for i := range gotStrs {
		if gotStrs[i] != wantStrs[i] {
			t.Fatalf("mappings = %v, want %v", gotStrs, wantStrs)
		}
	}
}

// TestAssignChildRenumbers pins the pure allocator's rules (GH#4796,
// bd-28sb25): minimal roots (a candidate inside another candidate's subtree
// maps by prefix, never allocates), deterministic parent/suffix ordering, and
// allocation above every high-water mark. The exact-boundary subtree mapping
// never touches P.710, and a destination that is taken or too long rejects
// the whole plan rather than proposing a repair that cannot be applied.
func TestAssignChildRenumbers(t *testing.T) {
	t.Run("report fixture: both sides collide, local holds the extra child", func(t *testing.T) {
		mappings, floors, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-rnp.71", "bd-rnp.72"},
			localIDs:   setOf("bd-rnp.71", "bd-rnp.72", "bd-rnp.73"),
			// 73 is the local counter; 72 the remote's; the used suffixes 71-73
			// fold in, so both roots allocate above 73.
			floors: map[string]int64{"bd-rnp": 73},
			taken:  setOf("bd-rnp.71", "bd-rnp.72", "bd-rnp.73"),
		})
		if err != nil {
			t.Fatalf("assignChildRenumbers: %v", err)
		}
		wantMappings(t, mappings, []storage.ChildRenumberMapping{
			{OldID: "bd-rnp.71", NewID: "bd-rnp.74", Root: true},
			{OldID: "bd-rnp.72", NewID: "bd-rnp.75", Root: true},
		})
		if floors["bd-rnp"] != 75 {
			t.Errorf("counter floor = %d, want 75", floors["bd-rnp"])
		}
	})

	t.Run("minimal roots and exact-boundary subtree", func(t *testing.T) {
		mappings, floors, err := assignChildRenumbers(renumberAllocInput{
			// P.71.2 is a candidate too (both tips hold it) but sits inside
			// P.71's subtree: it must map by prefix, not allocate.
			candidates: []string{"bd-st.71", "bd-st.71.2", "bd-st.72"},
			localIDs:   setOf("bd-st.71", "bd-st.71.2", "bd-st.71.2.3", "bd-st.72", "bd-st.710"),
			floors:     map[string]int64{"bd-st": 73},
			taken:      setOf("bd-st.71", "bd-st.71.2", "bd-st.72", "bd-st.710"),
		})
		if err != nil {
			t.Fatalf("assignChildRenumbers: %v", err)
		}
		wantMappings(t, mappings, []storage.ChildRenumberMapping{
			{OldID: "bd-st.71", NewID: "bd-st.74", Root: true},
			{OldID: "bd-st.71.2", NewID: "bd-st.74.2"},
			{OldID: "bd-st.71.2.3", NewID: "bd-st.74.2.3"},
			{OldID: "bd-st.72", NewID: "bd-st.75", Root: true},
		})
		if floors["bd-st"] != 75 {
			t.Errorf("counter floor = %d, want 75", floors["bd-st"])
		}
		// The exact boundary: bd-st.710 is NOT bd-st.71's descendant.
		for _, m := range mappings {
			if m.OldID == "bd-st.710" || m.NewID == "bd-st.710" {
				t.Errorf("mapping crossed the exact prefix boundary: %+v", m)
			}
		}
	})

	t.Run("permuted candidates sort by parent then suffix", func(t *testing.T) {
		mappings, _, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-pm.72", "bd-pm.71", "bd-pm.73"},
			localIDs:   setOf("bd-pm.71", "bd-pm.72", "bd-pm.73"),
			floors:     map[string]int64{"bd-pm": 73},
			taken:      setOf("bd-pm.71", "bd-pm.72", "bd-pm.73"),
		})
		if err != nil {
			t.Fatalf("assignChildRenumbers: %v", err)
		}
		// Sorted allocation gives the lowest colliding suffix the lowest
		// fresh suffix: 71->74, 72->75, 73->76, independent of input order.
		wantMappings(t, mappings, []storage.ChildRenumberMapping{
			{OldID: "bd-pm.71", NewID: "bd-pm.74", Root: true},
			{OldID: "bd-pm.72", NewID: "bd-pm.75", Root: true},
			{OldID: "bd-pm.73", NewID: "bd-pm.76", Root: true},
		})
	})

	t.Run("multiple parents allocate on their own floors", func(t *testing.T) {
		mappings, floors, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-q.5", "bd-m.71"},
			localIDs:   setOf("bd-q.5", "bd-m.71"),
			floors:     map[string]int64{"bd-q": 2, "bd-m": 73},
			taken:      setOf("bd-q.5", "bd-m.71"),
		})
		if err != nil {
			t.Fatalf("assignChildRenumbers: %v", err)
		}
		wantMappings(t, mappings, []storage.ChildRenumberMapping{
			{OldID: "bd-m.71", NewID: "bd-m.74", Root: true},
			{OldID: "bd-q.5", NewID: "bd-q.3", Root: true},
		})
		if floors["bd-q"] != 3 || floors["bd-m"] != 74 {
			t.Errorf("counter floors = %v, want bd-q:3 bd-m:74", floors)
		}
	})

	t.Run("taken destination advances the allocation", func(t *testing.T) {
		mappings, floors, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-tk.3"},
			localIDs:   setOf("bd-tk.3"),
			floors:     map[string]int64{"bd-tk": 2},
			// bd-tk.4 is retired on the wisp plane; the allocator must hand
			// out 5, not collide.
			taken: setOf("bd-tk.3", "bd-tk.4"),
		})
		if err != nil {
			t.Fatalf("assignChildRenumbers: %v", err)
		}
		wantMappings(t, mappings, []storage.ChildRenumberMapping{
			{OldID: "bd-tk.3", NewID: "bd-tk.5", Root: true},
		})
		if floors["bd-tk"] != 5 {
			t.Errorf("counter floor = %d, want 5 (raised past the taken suffix)", floors["bd-tk"])
		}
	})

	t.Run("taken descendant destination rejects the plan", func(t *testing.T) {
		_, _, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-dt.71"},
			localIDs:   setOf("bd-dt.71", "bd-dt.71.2"),
			floors:     map[string]int64{"bd-dt": 73},
			taken:      setOf("bd-dt.71", "bd-dt.74.2"),
		})
		if err == nil {
			t.Fatal("assignChildRenumbers accepted a plan whose descendant destination is taken")
		}
	})

	t.Run("id length limit rejects the plan", func(t *testing.T) {
		parent := strings.Repeat("x", 253)
		_, _, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{parent + ".9"},
			localIDs:   setOf(parent + ".9"),
			floors:     map[string]int64{parent: 9},
			taken:      setOf(parent + ".9"),
		})
		if err == nil {
			t.Fatal("assignChildRenumbers accepted a mapped id past the issues.id limit")
		}
	})

	t.Run("non-hierarchical candidate rejects the plan", func(t *testing.T) {
		_, _, err := assignChildRenumbers(renumberAllocInput{
			candidates: []string{"bd-flat"},
			localIDs:   setOf("bd-flat"),
		})
		if err == nil {
			t.Fatal("assignChildRenumbers accepted a non-hierarchical candidate")
		}
	})
}

func setOf(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
