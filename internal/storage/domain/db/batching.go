package db

// forEachIDBatch calls fn once per queryBatchSize-sized slice of ids, in
// order, stopping at the first error. The bulk readers use it to keep every
// IN (...) list bounded: Dolt's cost per IN-list placeholder is super-linear,
// so one statement over N ids is far slower than ceil(N/queryBatchSize)
// statements over queryBatchSize each (measured on a 19k-issue rig: the same
// labels rows took 31s as one statement and 0.8s as 98 batched ones).
//
// A given id lands in exactly one batch, so per-id result groups — and the
// ORDER BY within each group — survive being merged across batches.
//
// Read paths only: the DeleteAllForIDs loops keep their own deleteBatchSize
// and are deliberately left open-coded.
func forEachIDBatch(ids []string, fn func(batch []string) error) error {
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
