// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/doc.go@49d1df2f6)
// to OSS beads under the MIT license.
// Package wire is the transport core of the `http` storage backend: one
// base-URL client that speaks the v0 surface `bd serve` publishes
// (internal/httpapi/spec/openapi.v0.yaml), and nothing else.
//
// It owns four things and deliberately owns no more:
//
//   - The TRANSPORT. One request, one bounded response, JSON in and JSON out
//     of the canonical types. Redirects are refused rather than followed, so a
//     30x can never replay the Authorization header to another host, and every
//     response body is read under a byte cap, so a wrong URL answering with a
//     tarball is an error instead of the client's whole heap.
//   - The CREDENTIAL seam. CredentialProvider is the interface the activation
//     layer implements; this package consumes it and implements the client
//     half of the server's rotation contract — exactly one retry after one 401.
//   - The PROBLEM mapping. Every non-2xx is an RFC 9457 problem+json document,
//     and `code` is the only member this package dispatches on. Each code maps
//     to the canonical issueops sentinel the local store would have returned
//     for the same refusal, so a caller classifies a remote refusal with the
//     same errors.Is/errors.As arm it already uses locally.
//   - The CAPABILITY handshake. GET /v0/beads/context once per client, an
//     api_version gate, a workspace-identity check, and the two-speed
//     pre-flight policy: the five baseline operations dispatch straight, every
//     post-baseline operation consults the cached capability list first.
//
// What it does NOT own: the storage.DoltStorage implementation, the request
// encoders that turn issueops requests into query parameters (internal/httpclient/encode;
// see engdocs/design/http-divergence-ledger.md for the full divergence
// inventory), the refusal taxonomy's user-facing text, and the default
// endpoint/credential sources. Those are separate packages that consume this
// one.
//
// The package carries no build tag: it compiles and its unit gates run in the
// default PR lane regardless of which storage backend a given `bd` build
// wires in at the command layer.
package wire
