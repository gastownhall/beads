package rpc

// This file has been refactored into multiple files for better organization:
// - server_core.go: Server struct definition and NewServer constructor
// - server_lifecycle_conn.go: Server lifecycle (Start, Stop, WaitReady) and connection handling
// - server_cache_storage.go: Storage caching, eviction, and memory pressure management
// - server_routing_validation_diagnostics.go: Request routing, validation, and diagnostics
// - server_issues_epics.go: Issue CRUD operations and epic status handling
// - server_labels_deps_comments.go: Labels, dependencies, and comments operations
// - server_compact.go: Issue compaction operations
// - server_export_import_auto.go: Export, import, and auto-import operations
