package wireshape

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/spec"
)

// Entry is one member of one schema reachable from an operation's response.
// Schema is the component's name (or, for a member whose own value is an
// inline object or array with no $ref of its own, a synthetic dotted path
// rooted at the nearest named schema) so that two members with the same name
// on different schemas are never confused with one another.
type Entry struct {
	Schema   string   `json:"schema"`
	Member   string   `json:"member"`
	Type     string   `json:"type"`
	Format   string   `json:"format,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Required bool     `json:"required"`
}

// Digest is the golden document: the wire revision it was computed against,
// plus every member entry, sorted for a stable diff.
type Digest struct {
	WireRevision int     `json:"wire_revision"`
	Entries      []Entry `json:"entries"`
}

var httpVerbs = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true, "patch": true,
	"head": true, "options": true, "trace": true,
}

// Compute walks the embedded OpenAPI document and builds the digest for the
// current revision. wireRevision is passed in rather than read from a
// constant here so this package carries no dependency on internal/httpapi
// (which would otherwise be a cycle: internal/httpapi imports nothing from
// here, and this package must stay that way to run from the standalone
// gendigest command without pulling in the server).
func Compute(wireRevision int) (Digest, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		return Digest{}, fmt.Errorf("parse embedded openapi document: %w", err)
	}

	c := &collector{doc: doc, visited: map[string]bool{}, byKey: map[string]Entry{}}

	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		methods, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, raw := range methods {
			if !httpVerbs[strings.ToLower(method)] {
				continue
			}
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			responses, _ := op["responses"].(map[string]any)
			for _, rawResp := range responses {
				respMap, ok := rawResp.(map[string]any)
				if !ok {
					continue
				}
				respMap = c.resolveAny(respMap)
				content, _ := respMap["content"].(map[string]any)
				for _, rawMedia := range content {
					media, ok := rawMedia.(map[string]any)
					if !ok {
						continue
					}
					schemaNode, ok := media["schema"].(map[string]any)
					if !ok {
						continue
					}
					c.walk(schemaNode, "")
				}
			}
		}
	}

	entries := make([]Entry, 0, len(c.byKey))
	for _, e := range c.byKey {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Schema != entries[j].Schema {
			return entries[i].Schema < entries[j].Schema
		}
		return entries[i].Member < entries[j].Member
	})
	return Digest{WireRevision: wireRevision, Entries: entries}, nil
}

type collector struct {
	doc     map[string]any
	visited map[string]bool
	byKey   map[string]Entry // "schema\x00member" -> Entry
}

// lookup resolves one local $ref to the node it names. It returns nil for
// anything this document does not use (a remote ref, or a path with no node).
func lookup(doc map[string]any, ref string) map[string]any {
	rest := strings.TrimPrefix(ref, "#/")
	var cur any = doc
	for _, part := range strings.Split(rest, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	m, _ := cur.(map[string]any)
	return m
}

// schemaRefName reports the component schema name a $ref names, or "" when
// the ref points somewhere else (components.responses, for instance).
func schemaRefName(ref string) string {
	const prefix = "#/components/schemas/"
	name, ok := strings.CutPrefix(ref, prefix)
	if !ok {
		return ""
	}
	return name
}

// resolveAny follows a $ref chain to its concrete node, however many hops it
// takes (a response $ref lands on a response object; it is never itself a
// further $ref in this document, but the walk tolerates it either way).
func (c *collector) resolveAny(node map[string]any) map[string]any {
	seen := 0
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		next := lookup(c.doc, ref)
		if next == nil {
			return node
		}
		node = next
		seen++
		if seen > 32 {
			// Defensive only: this document has no ref cycle, and 32 hops is
			// far beyond anything a hand-written spec would ever chain.
			return node
		}
	}
}

func toStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, fmt.Sprint(e))
	}
	sort.Strings(out)
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// walk records every property of the schema node reaches (following $refs and
// flattening allOf), then recurses into each property's own object or array
// shape. label names an anonymous (non-$ref) node for entries recorded under
// it; a $ref always overrides it with the component's own name.
//
// Every NAMED schema is walked at most once (the visited guard), which is
// what makes a cycle between named schemas (and there is at least the
// potential for one as this document grows) a no-op rather than a stack
// overflow, and what makes the digest the same whichever operation reaches a
// shared schema first.
func (c *collector) walk(node map[string]any, label string) {
	name := label
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			break
		}
		if n := schemaRefName(ref); n != "" {
			name = n
		}
		next := lookup(c.doc, ref)
		if next == nil {
			return
		}
		node = next
	}

	if allOf, ok := node["allOf"].([]any); ok {
		mergedProps := map[string]any{}
		var mergedRequired []any
		for _, rawSub := range allOf {
			subMap, ok := rawSub.(map[string]any)
			if !ok {
				continue
			}
			sub := c.resolveAny(subMap)
			if props, ok := sub["properties"].(map[string]any); ok {
				for k, v := range props {
					mergedProps[k] = v
				}
			}
			if req, ok := sub["required"].([]any); ok {
				mergedRequired = append(mergedRequired, req...)
			}
		}
		node = map[string]any{
			"type":       "object",
			"properties": mergedProps,
			"required":   mergedRequired,
		}
	}

	if name != "" {
		if c.visited[name] {
			return
		}
		c.visited[name] = true
	}

	if props, ok := node["properties"].(map[string]any); ok {
		required := map[string]bool{}
		for _, r := range toStringSlice(node["required"]) {
			required[r] = true
		}
		members := make([]string, 0, len(props))
		for m := range props {
			members = append(members, m)
		}
		sort.Strings(members)
		for _, member := range members {
			propRaw := props[member]
			propMap, ok := propRaw.(map[string]any)
			if !ok {
				continue
			}
			resolved := c.resolveAny(propMap)
			entry := Entry{
				Schema:   name,
				Member:   member,
				Type:     asString(resolved["type"]),
				Format:   asString(resolved["format"]),
				Enum:     toStringSlice(resolved["enum"]),
				Required: required[member],
			}
			c.byKey[entry.Schema+"\x00"+entry.Member] = entry

			switch entry.Type {
			case "object":
				c.walk(propMap, name+"."+member)
			case "array":
				if items, ok := resolved["items"].(map[string]any); ok {
					c.walk(items, name+"."+member+"[]")
				}
			}
		}
		return
	}

	if asString(node["type"]) == "array" {
		if items, ok := node["items"].(map[string]any); ok {
			itemLabel := name
			if itemLabel == "" {
				itemLabel = label
			}
			c.walk(items, itemLabel+"[]")
		}
	}
}
