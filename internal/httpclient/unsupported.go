package httpclient

// Regeneration: S3 reconciliation (gc native-program, 2026-10) replaced
// bd-enterprise's lifted generator and its hand-maintained -skip allowlist
// with internal/storage/unsupportedgen, a from-scratch tool written for this
// client. It takes no skip list: it parses this package's own non-test
// sources for every method already declared on *Store (the wire-backed role
// accessors in accessors.go, the off-role raw methods with a v0 mapping in
// offrole.go and lifecycle.go's CloseIssue, the commit family and Close in
// store.go, the metadata trio in metadata.go, and vocabulary.go's reads) and
// generates a stub for every OTHER storage.DoltStorage method. A hand-written
// method and a generated one can never collide on the same selector, and
// there is no allowlist to keep in sync by hand — adding a hand-written
// override just removes that method from the next regeneration automatically.
//
// Never hand-edit unsupported_gen.go. Regenerate with `go generate ./...`
// after adding or removing a hand-written DoltStorage method; store.go's
// `var _ storage.DoltStorage = (*Store)(nil)` assertion is the drift tripwire
// against a storage.DoltStorage interface change the regeneration itself
// doesn't already surface (an interface method neither hand-written nor
// generated fails that assertion at compile time).
//
// The tool path is one level up because this package sits beside
// internal/storage, both direct children of internal/.
//
//go:generate go run ../storage/unsupportedgen -type DoltStorage -pkg httpclient -receiver Store -out unsupported_gen.go
