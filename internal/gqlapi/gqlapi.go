// Package gqlapi answers read-only GraphQL documents over the issue roles.
//
// It does no shaping of its own. Every row comes from an issueops role, and
// the role decides what a row contains; this package only chooses which
// returned fields to print and which optional role reads to ask for. It
// imports no storage, transport or CLI package, so the same roles serve it on
// every route.
package gqlapi

import (
	"context"
	_ "embed"

	graphql "github.com/graph-gophers/graphql-go"

	"github.com/steveyegge/beads/issueops"
)

//go:embed schema.graphqls
var schemaSDL string

// Limits applied to every document. graphql-go leaves length and depth
// unchecked by default.
const (
	maxQueryLength = 16384
	maxDepth       = 8
)

// Roles are the read roles a document runs against.
type Roles struct {
	Reader  issueops.Reader
	Querier issueops.Querier
}

// Execute runs one document against roles and returns graphql-go's response.
// Errors in the document or from a role are in the response, never returned
// separately; the caller decides how to print it.
func Execute(ctx context.Context, roles Roles, document string, variables map[string]any) *graphql.Response {
	schema, err := graphql.ParseSchema(schemaSDL, &queryResolver{roles: roles, cache: make(map[getKey]*issueops.IssueDetails)},
		graphql.MaxQueryLength(maxQueryLength),
		graphql.MaxDepth(maxDepth),
		graphql.MaxParallelism(1),
	)
	if err != nil {
		// The schema and resolvers are compiled into bd, so this is a bug in
		// bd, not in the document.
		panic("gqlapi: invalid schema: " + err.Error())
	}
	resp := schema.Exec(ctx, document, "", variables)
	for _, err := range resp.Errors {
		if err.Message == "graphql-ws protocol header is missing" {
			err.Message = "subscription operations are not supported by bd gql"
		}
	}
	return resp
}
