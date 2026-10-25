package db

import (
	"errors"
	"fmt"
	"testing"
)

// collectBatches runs forEachIDBatch over ids and returns the batches it
// handed to fn, copied so a later batch cannot alias an earlier one.
func collectBatches(t *testing.T, ids []string) [][]string {
	t.Helper()
	var got [][]string
	if err := forEachIDBatch(ids, func(batch []string) error {
		got = append(got, append([]string(nil), batch...))
		return nil
	}); err != nil {
		t.Fatalf("forEachIDBatch: %v", err)
	}
	return got
}

// TestForEachIDBatchDeliversEachIDOnce pins the property the bulk readers'
// merges rely on: an id reaches exactly one batch. A single IN (...) list
// is a set, so duplicate input ids never produced duplicate rows before the
// readers were batched; an id repeated across a batch boundary must not
// make its rows (labels, comments, dependency records, issues) appear twice
// now.
func TestForEachIDBatchDeliversEachIDOnce(t *testing.T) {
	const n = queryBatchSize*2 + 50
	unique := make([]string, n)
	for i := range unique {
		unique[i] = fmt.Sprintf("bd-%03d", i)
	}

	cases := map[string][]string{
		// The whole list twice: every id appears in two different batches.
		"repeated across batches": append(append([]string(nil), unique...), unique...),
		// Duplicates inside one batch and straddling the first boundary.
		"within and across a boundary": append(append([]string{unique[0], unique[0]}, unique...),
			unique[queryBatchSize-1], unique[queryBatchSize], unique[n-1]),
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			before := append([]string(nil), ids...)
			batches := collectBatches(t, ids)

			var flat []string
			for _, b := range batches {
				if len(b) == 0 || len(b) > queryBatchSize {
					t.Fatalf("batch size %d, want 1..%d", len(b), queryBatchSize)
				}
				flat = append(flat, b...)
			}
			if len(flat) != n {
				t.Fatalf("delivered %d ids, want each of the %d distinct ids once", len(flat), n)
			}
			for i, id := range flat {
				if id != unique[i] {
					t.Fatalf("position %d: got %s, want %s (first-seen order)", i, id, unique[i])
				}
			}
			if want := (n + queryBatchSize - 1) / queryBatchSize; len(batches) != want {
				t.Fatalf("got %d batches, want %d", len(batches), want)
			}
			for i := range ids {
				if ids[i] != before[i] {
					t.Fatalf("caller's slice was modified at %d", i)
				}
			}
		})
	}
}

func TestForEachIDBatchUniqueInputUnchanged(t *testing.T) {
	ids := []string{"bd-c", "bd-a", "bd-b"}
	batches := collectBatches(t, ids)
	if len(batches) != 1 || fmt.Sprint(batches[0]) != fmt.Sprint(ids) {
		t.Fatalf("got %v, want one batch %v", batches, ids)
	}
	if got := collectBatches(t, nil); len(got) != 0 {
		t.Fatalf("empty input issued %d batches, want 0", len(got))
	}
}

func TestForEachIDBatchStopsAtFirstError(t *testing.T) {
	ids := make([]string, queryBatchSize+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%03d", i)
	}
	boom := errors.New("boom")
	calls := 0
	err := forEachIDBatch(ids, func([]string) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want boom after 1 call", err, calls)
	}
}
