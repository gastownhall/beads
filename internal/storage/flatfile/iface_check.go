package flatfile

import "github.com/steveyegge/beads/internal/storage"

// Compile-time interface satisfaction checks.
var _ storage.DoltStorage = (*FlatFileStore)(nil)
var _ storage.StoreLocator = (*FlatFileStore)(nil)
var _ storage.LifecycleManager = (*FlatFileStore)(nil)
var _ storage.ReadyWorkCounter = (*FlatFileStore)(nil)
