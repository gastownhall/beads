package storage

import "github.com/steveyegge/beads/issueops"

// DetailBatchReader forwards the read role unchanged: reads fire no completion hooks.
func (h *HookFiringStore) DetailBatchReader() (issueops.DetailBatchReader, error) {
	return h.inner.DetailBatchReader()
}
