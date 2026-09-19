// Package spec holds the hand-written OpenAPI document for the bd serve HTTP
// API and embeds it into the binary.
//
// The document is the source of truth for the wire contract. The Go types in
// internal/httpapi/apigen are generated from it; the parity and bijection
// tests parse the embedded bytes, so they check the document that actually
// ships rather than a copy on disk.
package spec

import (
	"bytes"
	_ "embed"
)

//go:embed openapi.v0.yaml
var openAPIV0 []byte

// OpenAPIV0 returns the OpenAPI 3.0.3 document describing the /v0 surface.
// The returned slice is a copy: callers may not mutate the embedded bytes.
func OpenAPIV0() []byte {
	return bytes.Clone(openAPIV0)
}
