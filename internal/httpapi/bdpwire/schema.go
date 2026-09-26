package bdpwire

import (
	"bytes"
	_ "embed"
)

// Pin is the gastownhall/bdp commit every vendored file under schema/ was
// taken from. It is the plan's §0 pin (BDP_BEAD_GRAPH_PLAN.md), repeated here
// so code can name it; pin_test.go asserts it equals the `commit:` line of
// schema/PROVENANCE, so the two cannot drift apart silently.
const Pin = "19923f5bb6cc3f4ee4c508e36df3bd4c5c52344b"

// SchemaID is the bundle's canonical `$id`. It is a protocol identity —
// compared exactly, never dereferenced — and the base the pinned matrix's
// `json-schema` assertions name definitions against, as "#/$defs/<name>".
const SchemaID = "https://github.com/gastownhall/bdp/schemas/bdp-v0.schema.json"

// ProblemTypePrefix is the BDP v0 problem-family prefix: a problem's `type`
// is this prefix followed by the family suffix (ReadProblemCode.Family).
const ProblemTypePrefix = "https://github.com/gastownhall/bdp/problems/"

// ProblemMediaType is the RFC 9457 media type every BDP problem body is
// served as; the pinned matrix asserts it on every problem response.
const ProblemMediaType = "application/problem+json"

// ServiceDescRel is the registered link relation (RFC 8631) the Scope
// response carries in its Link field to reach the discovery document. It is
// the one required machine entry point: a client follows it and never reads
// discovery metadata out of the Scope body.
const ServiceDescRel = "service-desc"

//go:embed schema/bdp-v0.schema.json
var schemaBundle []byte

// SchemaBundle returns the vendored normative bundle. The returned slice is a
// copy: callers may not mutate the embedded bytes. Tests in this package and,
// at P2, the route-grammar parity test in internal/httpapi read it from here
// so they check the artifact that actually ships, not a copy on disk.
func SchemaBundle() []byte {
	return bytes.Clone(schemaBundle)
}
