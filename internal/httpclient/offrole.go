// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/offrole.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

// The off-role raw methods design D8 keeps off the unsupported allowlist. They
// are not role accessors — cmd/bd still calls them directly at tip — but each
// has a v0 operation behind it, so refusing them would refuse a command the
// wire can serve. Like the role accessors, they are hand-written and move OUT of
// this file to sit beside the surface they serve the moment their bead wires
// them (precedent resolve.go/bridge.go/vocabulary.go).
//
// This file is now a TOMBSTONE: it holds no methods. It stays because the
// retirement path is worth writing down where it happened. (There is no skip
// list to keep in step: unsupportedgen derives the shell from the methods
// declared on *Store, so a read that moved out of here needed only a
// regeneration.)
//
// The reads this file used to hold are SERVED, each beside the surface it
// belongs to: the four getIssue riders — GetLabels, GetDependenciesWithMetadata,
// GetDependentsWithMetadata and GetIssueComments — in detail_reads.go, and the
// anchored GetDependencyRecords in roles_graph.go beside the EdgeReader role it
// shares its operation with (ga-b8ddd.30). The verbatim-row question their flip
// waited on — whether the wire surfaces unresolved depends_on_id rows the
// GH#5005 `bd dep remove` guard must see — was answered by the dual run in
// served_detail_reads_test.go: it does, so there is no L17 degradation to
// ledger.
//
// The earlier tenants left the same way: SearchIssues and GetConfig to
// resolve.go with the id-resolution probes, the ready bridge and the raw
// GetIssue probe to bridge.go, and the settings, statistics and vocabulary reads
// to vocabulary.go.
//
// Everything NOT here and not in accessors.go, store.go or metadata.go is on the
// generated shell and refuses permanently — including the batch and unanchored
// variants (GetLabelsForIssues, GetDependencyRecordsForIssues, GetDependencies,
// GetDependents, GetNextChildID) and the VersionReconciler, Bootstrapper and
// InitVerifier accessors.
