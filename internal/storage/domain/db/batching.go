package db

// forEachIDBatch calls fn once per queryBatchSize-sized slice of ids, in
// order, stopping at the first error. The bulk readers use it to keep every
// IN (...) list bounded: Dolt's cost per IN-list placeholder is super-linear,
// so one statement over N ids is far slower than ceil(N/queryBatchSize)
// statements over queryBatchSize each (measured on a 19k-issue rig: the same
// labels rows took 31s as one statement and 0.8s as 98 batched ones).
//
// Duplicate ids are dropped first, keeping first-seen order, so a given id
// lands in exactly one batch. A single IN list is a set; without this, an id
// repeated across a batch boundary would return its rows once per batch.
// With it, per-id result groups (and the ORDER BY within each group) survive
// being merged across batches.
//
// Read paths only: the DeleteAllForIDs loops keep their own deleteBatchSize
// and are deliberately left open-coded.
func forEachIDBatch(ids []string, fn func(batch []string) error) error {
	ids = distinctIDs(ids)
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := fn(ids[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// distinctIDs returns ids without repeats, in first-seen order. It returns
// ids itself when there are none to drop and never modifies the caller's
// slice.
func distinctIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		if _, dup := seen[id]; dup {
			out := append(make([]string, 0, len(ids)-1), ids[:i]...)
			for _, rest := range ids[i+1:] {
				if _, dup := seen[rest]; !dup {
					seen[rest] = struct{}{}
					out = append(out, rest)
				}
			}
			return out
		}
		seen[id] = struct{}{}
	}
	return ids
}
