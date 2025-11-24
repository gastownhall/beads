// Package sqlite implements the storage interface using SQLite.
//
// This package has been split into focused files for better maintainability:
//
// Core storage components:
//   - store.go: SQLiteStorage struct, New() constructor, initialization logic,
//     and database utility methods (Close, Path, IsClosed, UnderlyingDB, etc.)
//   - queries.go: Issue CRUD operations including CreateIssue, GetIssue,
//     UpdateIssue, DeleteIssue, DeleteIssues, SearchIssues
//   - config.go: Configuration and metadata management (SetConfig, GetConfig,
//     SetMetadata, GetMetadata, OrphanHandling)
//   - comments.go: Comment operations (AddIssueComment, GetIssueComments)
//
// Supporting components:
//   - schema.go: Database schema definitions
//   - migrations.go: Schema migration logic
//   - dependencies.go: Dependency management (AddDependency, RemoveDependency, etc.)
//   - labels.go: Label operations
//   - events.go: Event tracking
//   - dirty.go: Dirty issue tracking for incremental exports
//   - batch_ops.go: Batch operations for bulk imports
//   - hash_ids.go: Hash-based ID generation
//   - validators.go: Input validation functions
//   - util.go: Utility functions
//
// Historical notes (bd-0a43):
// Prior to this refactoring, sqlite.go was 1050+ lines containing all storage logic.
// The monolithic structure made it difficult to navigate and understand specific
// functionality. This split maintains all existing functionality while improving
// code organization and discoverability.
package sqlite
