// Package wireshape is the CI drift gate for ContextResponse.wire_revision.
//
// It computes a digest of every member reachable from any response schema AND
// any request body schema in internal/httpapi/spec/openapi.v0.yaml — walking
// $refs through components.schemas and components.responses, and recursing
// through allOf/oneOf — recording, per member, the (schema, member, type,
// format, enum, required, nullable) tuple the document promises, plus the
// item shape of an array member and the value shape of an
// additionalProperties map. TestWireShapeDigest compares that digest against
// the committed golden in testdata/golden.json and fails on any difference: a
// member added is fine (an additive change never bumps wire_revision), but a
// member's type, format, enum vocabulary, required-ness, or nullability
// changing — or a member disappearing — is exactly the class of change
// internal/httpapi/wire_revision.go's CurrentWireRevision exists to gate, and
// the golden's own "wire_revision" field is compared too, so a shape change
// recorded against the OLD revision number still fails.
//
// Regenerate the golden after a deliberate, revision-bumped change with:
//
//	go run ./internal/httpapi/wireshape/cmd/gendigest
//
// That command itself refuses to write a changed or removed entry unless
// wire_revision has moved past what the existing golden recorded, and refuses
// to create a golden that does not exist yet unless run with -init — a
// missing file is not treated as permission to start a fresh history
// silently. Never hand-edit testdata/golden.json.
package wireshape
