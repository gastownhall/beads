// Package memoryops describes the workspace's PERSISTENT MEMORY PLANE: the
// keyed notes `bd remember`, `bd recall`, `bd forget` and `bd memories` store
// and read, and that `bd prime` injects into a session.
//
// It is a sibling of issueops rather than more verbs on
// issueops.WorkspaceConfig, and the reason is not that the rule forbids
// appending (it does): memories are not settings. They ride in the same config
// table but they are USER DATA under a reserved namespace
// (internal/storage/kvkeys), they have their own merge class — the storage
// merge resolver auto-resolves a config conflict with --theirs only when every
// conflicted key carries the kv.memory. prefix
// (internal/storage/versioncontrolops/mergesettle.go:639-671), so
// convergent-on-pull is part of what a memory MEANS and no settings row means
// that — and they have a found/not-found user contract the settings plane has
// no use for.
//
// THE STORAGE ENCODING IS NOT IN THIS PACKAGE, and that is deliberate. A
// memory named "dolt-phantoms" is stored under the config key
// "kv.memory.dolt-phantoms", but every request and result here carries the USER
// key. kvkeys is the single source of truth for the prefix and the
// implementations are the only code that spells it; naming it in a leaf type
// would make the encoding part of the caller's vocabulary and put a second copy
// one rename away from drifting. The encoding is stated here in prose because
// it is part of what the contract MEANS — it is why a memory converges on pull
// and a setting does not — not because a caller has to construct it.
//
// WHAT THIS PACKAGE IMPORTS: stdlib, plus
// github.com/steveyegge/beads/beadserrors for the error sentinel and nothing
// else. That is narrower than the issueops leaf rule ("internal/types and
// stdlib"): a memory is a string under a string key, so nothing in
// internal/types is needed here and importing it would only invite issue-shaped
// types into a plane that has none. The sentinel is ALIASED rather than
// redeclared — see errors.go.
//
// It aliases through beadserrors rather than through issueops, which is where
// that value used to live. Reaching into the issue package for it worked and
// kept errors.Is identity, but it put issueops in this leaf's dependency graph
// and internal/types behind it — a claim that the memory plane sits downstream
// of the issue plane, when the two are siblings over one config table. The
// shared vocabulary moved down instead. beadserrors imports stdlib only, and a
// depguard rule keeps it that way.
//
// THERE IS NO PAGE HERE. Memories are a keyed namespace a workspace holds tens
// of, exactly like settings, so List answers with a map: no order, no limit, no
// cursor. A front door that prints them sorts the keys, and both do today. If
// this plane ever grows to thousands of rows, a paged reader is a new role
// conversation and not a retrofit of this one.
package memoryops
