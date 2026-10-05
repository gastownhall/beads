// Package wireshape is the CI drift gate for ContextResponse.wire_revision.
//
// It computes a digest of every member reachable from any response schema in
// internal/httpapi/spec/openapi.v0.yaml — walking $refs through
// components.schemas and components.responses — recording, per member, the
// (schema, member, type, format, enum, required) tuple the document promises.
// TestWireShapeDigest compares that digest against the committed golden in
// testdata/golden.json and fails on any difference: a member added is fine (an
// additive change never bumps wire_revision), but a member's type, format,
// enum vocabulary, or required-ness changing — or a member disappearing — is
// exactly the class of change internal/httpapi/wire_revision.go's
// CurrentWireRevision exists to gate, and the golden's own
// "wire_revision" field is compared too, so a shape change recorded against
// the OLD revision number still fails.
//
// Regenerate the golden after a deliberate, revision-bumped change with:
//
//	go run ./internal/httpapi/wireshape/cmd/gendigest
//
// Never hand-edit testdata/golden.json.
package wireshape
