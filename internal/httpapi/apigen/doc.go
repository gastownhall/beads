// Package apigen holds the Go types generated from the bd serve OpenAPI
// document (spec-first: internal/httpapi/spec/openapi.v0.yaml is the source of
// truth, this package is its output).
//
// Do NOT hand-edit types.gen.go — change the spec and run `make api-gen`.
// `make api-check` regenerates, fails on any diff, and runs the spec tests, so
// an un-regenerated spec edit cannot merge.
//
// Types only: no server stubs, no router, no request validation. The handlers
// stay hand-written and framework-free, which is also why the query decoders
// must reject unknown parameters explicitly (spec, "Unknown query parameters
// are rejected") — a types-only decoder ignores them by default.
//
//go:generate go tool oapi-codegen -generate types,skip-prune -package apigen -o types.gen.go ../spec/openapi.v0.yaml
package apigen
