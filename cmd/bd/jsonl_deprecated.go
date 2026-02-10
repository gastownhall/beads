package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// tempFileCounter provides unique IDs for concurrent temp file creation.
// Deprecated: leftover from JSONL sync.
var tempFileCounter atomic.Uint64

// jsonl_deprecated.go provides stub types and functions for the removed JSONL sync subsystem.
//
// The JSONL sync layer (autoflush, autoimport, export, import, flush_manager)
// has been removed. These stubs exist only to allow gradual cleanup of the
// many command files that still reference them. All functions are no-ops.
//
// TODO(dolt-transition): Remove this file after all JSONL sync references
// are cleaned up from command files.

// FlushManager is a deprecated stub type. JSONL sync has been removed.
type FlushManager struct {
	writeRecorded bool
}

// NewFlushManager is a deprecated no-op constructor.
func NewFlushManager(_ bool, _ time.Duration) *FlushManager { return &FlushManager{} }

// Shutdown is a deprecated no-op.
func (fm *FlushManager) Shutdown() error { return nil }

// MarkDirty is a deprecated no-op.
func (fm *FlushManager) MarkDirty(_ bool) {}

// FlushNow is a deprecated no-op.
func (fm *FlushManager) FlushNow() error { return nil }

// RecordWrite is a deprecated no-op.
func (fm *FlushManager) RecordWrite() {
	if fm != nil {
		fm.writeRecorded = true
	}
}

// DidWrite returns whether a write was recorded.
func (fm *FlushManager) DidWrite() bool {
	if fm == nil {
		return false
	}
	return fm.writeRecorded
}

// markDirtyAndScheduleFlush is a deprecated no-op. JSONL sync has been removed.
func markDirtyAndScheduleFlush() {}

// markDirtyAndScheduleFullExport is a deprecated no-op. JSONL sync has been removed.
func markDirtyAndScheduleFullExport() {}

// autoImportIfNewer is a deprecated no-op. JSONL sync has been removed.
func autoImportIfNewer() {}

// clearAutoFlushState is a deprecated no-op. JSONL sync has been removed.
func clearAutoFlushState() {}

// flushState is a deprecated stub type. JSONL sync has been removed.
type flushState struct {
	forceDirty      bool
	forceFullExport bool
}

// flushToJSONLWithState is a deprecated no-op. JSONL sync has been removed.
func flushToJSONLWithState(_ flushState) {}

// writeJSONLAtomic is a deprecated no-op. JSONL sync has been removed.
func writeJSONLAtomic(_ string, _ []*types.Issue) ([]string, error) { return nil, nil }

// validateJSONLIntegrity is a deprecated no-op. JSONL sync has been removed.
func validateJSONLIntegrity(_ context.Context, _ string) (bool, error) { return true, nil }

// readExistingJSONL is a deprecated no-op. JSONL sync has been removed.
func readExistingJSONL(_ string) (map[string]*types.Issue, error) { return nil, nil }

// getIssuesToExport is a deprecated no-op. JSONL sync has been removed.
func getIssuesToExport(_ context.Context, _ bool) ([]string, error) { return nil, nil }

// updateFlushExportMetadata is a deprecated no-op. JSONL sync has been removed.
func updateFlushExportMetadata(_ context.Context, _ storage.Storage, _ string) {}
