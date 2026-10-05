package wireshape

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/spec"
)

// Entry is one member of one schema reachable from an operation's response OR
// request body, OR one operation parameter (query, path or header). Schema is
// the component's name (or, for a member whose own value is an inline object
// or array with no $ref of its own, a synthetic dotted path rooted at the
// nearest named schema) so that two members with the same name on different
// schemas are never confused with one another. For a parameter, Schema is
// instead "param:"+operationId and Member is in+":"+name (see
// paramKey) — operation-scoped rather than schema-scoped, because two
// different operations' same-named "status" query parameter are two
// independent wire contracts even when today they happen to share a shape.
//
// The Item* and AdditionalProps* fields exist because a property's own
// Type/Format/Enum/Nullable describe the CONTAINER, not what it contains: a
// `type: array` property is always `Type: "array"` whether its items are
// strings or integers, and a `type: object` property with no `properties` of
// its own (a map, via `additionalProperties`) is always `Type: "object"`
// whether its values are strings or integers. Without these fields, changing
// Issue.labels' item type from string to integer, or IssueCount.groups'
// value type from integer to string, is invisible to this digest even though
// it is exactly the non-additive wire change CurrentWireRevision exists to
// gate — the other half of #6053 alongside the request-body coverage below.
type Entry struct {
	Schema   string   `json:"schema"`
	Member   string   `json:"member"`
	Type     string   `json:"type"`
	Format   string   `json:"format,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Required bool     `json:"required"`
	// Nullable records `nullable: true` on this member's own schema node. A
	// response member moving from always-present-when-returned to
	// sometimes-null is exactly as breaking to a client as the type itself
	// changing, and before this field nothing here could see it.
	Nullable bool `json:"nullable,omitempty"`

	// ItemType, ItemFormat, ItemEnum and ItemNullable describe a `type: array`
	// member's own items node. They are set whenever Type == "array" and the
	// items schema resolves to something, regardless of whether the items are
	// a scalar (only these fields describe it) or an object with its own
	// named members (which ALSO get walked and recorded as their own
	// Entry-ies under schema+"."+member+"[]", same as before).
	ItemType     string   `json:"item_type,omitempty"`
	ItemFormat   string   `json:"item_format,omitempty"`
	ItemEnum     []string `json:"item_enum,omitempty"`
	ItemNullable bool     `json:"item_nullable,omitempty"`

	// AdditionalPropsType and AdditionalPropsFormat describe a `type: object`
	// member's `additionalProperties` schema — the map-value shape of a
	// member like IssueCount.groups, which has no `properties` of its own.
	// Set only when additionalProperties is a schema (not the boolean `true`
	// or `false` some operations use for "no constraint" / "sealed").
	AdditionalPropsType   string `json:"additional_props_type,omitempty"`
	AdditionalPropsFormat string `json:"additional_props_format,omitempty"`

	// Style, Explode and Default are set only for parameter entries (see
	// paramKey): the serialization style OpenAPI uses to flatten an array or
	// object parameter into a query/path string, whether it repeats the name
	// per value or explodes/collapses, and the literal default value. A
	// server that starts expecting `label=a,b` breaks every client still
	// sending `label=a&label=b`, even though neither Type nor Enum changed —
	// exactly the gap the parameter digest closes.
	//
	// Style and Explode are the EFFECTIVE values, with OpenAPI 3.0's defaults
	// filled in where the document leaves them unset (see walkParam), so a
	// parameter that spells out its default is the same entry as one that
	// relies on it, while `explode: false` added to a form parameter that
	// relied on the implicit `true` is a changed entry.
	Style   string `json:"style,omitempty"`
	Explode bool   `json:"explode,omitempty"`
	Default string `json:"default,omitempty"`
}

// paramSchemaPrefix marks an Entry.Schema as naming an operation (via
// operationId) rather than an OpenAPI component schema. Grepping for this
// prefix is how a reader tells a parameter entry apart from a schema member
// entry in golden.json.
const paramSchemaPrefix = "param:"

// paramKey returns the Schema/Member pair a parameter entry is filed under:
// operation-scoped by operationId, then by location and name. Location is
// part of the key (not just the name) because OpenAPI allows the same name in
// two different locations on one operation (a path `id` and a query `id`
// would otherwise collide).
func paramKey(operationID, in, name string) (schema, member string) {
	return paramSchemaPrefix + operationID, in + ":" + name
}

// Digest is the golden document: the wire revision it was computed against,
// plus every member entry, sorted for a stable diff.
type Digest struct {
	WireRevision int     `json:"wire_revision"`
	Entries      []Entry `json:"entries"`
}

// CompareResult is the outcome of diffing two digests by "schema\x00member"
// key: Changed holds keys present in both whose Entry differs, Removed holds
// keys only want has, and Added holds keys only got has.
type CompareResult struct {
	Changed []string
	Removed []string
	Added   []string
}

// Compare diffs want (typically the committed golden) against got (typically
// a fresh Compute()). It is the one comparison TestWireShapeDigest and
// gendigest's SafeToWrite guard both need, extracted here so the write guard
// and the test can never disagree about what counts as a change.
func Compare(want, got Digest) CompareResult {
	wantByKey := map[string]Entry{}
	for _, e := range want.Entries {
		wantByKey[e.Schema+"\x00"+e.Member] = e
	}
	gotByKey := map[string]Entry{}
	for _, e := range got.Entries {
		gotByKey[e.Schema+"\x00"+e.Member] = e
	}

	var result CompareResult
	for key, w := range wantByKey {
		g, ok := gotByKey[key]
		if !ok {
			result.Removed = append(result.Removed, key)
			continue
		}
		if !reflect.DeepEqual(w, g) {
			result.Changed = append(result.Changed, key)
		}
	}
	for key := range gotByKey {
		if _, ok := wantByKey[key]; !ok {
			result.Added = append(result.Added, key)
		}
	}
	sort.Strings(result.Changed)
	sort.Strings(result.Removed)
	sort.Strings(result.Added)
	return result
}

// SafeToWrite is gendigest's write guard (review MEDIUM: "refuse to write
// changed or removed entries unless CurrentWireRevision is higher than the
// golden's recorded revision. Additive entries are allowed."). A pure
// addition is always safe regardless of revision; a changed or removed entry
// is only safe when candidate's WireRevision is strictly greater than
// golden's — the exact bump TestWireShapeDigest itself requires to go green,
// so gendigest can never be used to silently launder a failing test instead
// of fixing it. A candidate whose WireRevision is LOWER than golden's is
// refused whatever its entries: the revision table is append-only, so writing
// it would record a rollback the spec forbids.
func SafeToWrite(golden, candidate Digest) (bool, string) {
	if candidate.WireRevision < golden.WireRevision {
		return false, fmt.Sprintf(
			"refusing to write: wire_revision %d is lower than the existing golden's %d — wire_revision only "+
				"ever increases (the revision table in openapi.v0.yaml's wire_revision property is append-only), "+
				"so restore CurrentWireRevision (internal/httpapi/wire_revision.go) instead of writing the "+
				"golden backwards",
			candidate.WireRevision, golden.WireRevision)
	}
	cmp := Compare(golden, candidate)
	if len(cmp.Changed) == 0 && len(cmp.Removed) == 0 {
		return true, ""
	}
	if candidate.WireRevision > golden.WireRevision {
		return true, ""
	}
	return false, fmt.Sprintf(
		"refusing to write: %d changed and %d removed entr(ies) but wire_revision %d is not greater than "+
			"the existing golden's %d (changed=%v removed=%v) — bump CurrentWireRevision "+
			"(internal/httpapi/wire_revision.go) and the revision table in openapi.v0.yaml's "+
			"wire_revision property FIRST, then regenerate",
		len(cmp.Changed), len(cmp.Removed), candidate.WireRevision, golden.WireRevision, cmp.Changed, cmp.Removed)
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
//
// RESPONSE schemas, REQUEST BODY schemas, and operation PARAMETERS (query,
// path and header — see paramKey) are all walked. A request-body-only shape
// change — DeleteIssuesRequest.expected_version moving from string to
// integer, say — is exactly as much an undocumented break to an existing
// client as a response change, and the other half of #6053 was invisible
// here until request bodies were added alongside responses. Parameters are
// the same story again: a `limit` query parameter silently retyped,
// re-enumerated, or switched from optional to required never touched a
// response or request body schema, so it was invisible to this digest until
// parameter coverage was added.
func Compute(wireRevision int) (Digest, error) {
	return computeFrom(spec.OpenAPIV0(), wireRevision)
}

// computeFrom is Compute over any OpenAPI document, so this package's own
// tests can drive the walk with a synthetic document instead of the embedded
// one.
func computeFrom(document []byte, wireRevision int) (Digest, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(document, &doc); err != nil {
		return Digest{}, fmt.Errorf("parse openapi document: %w", err)
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
				c.walkContent(respMap)
			}

			if rb, ok := op["requestBody"].(map[string]any); ok {
				rb = c.resolveAny(rb)
				c.walkContent(rb)
			}

			operationID := asString(op["operationId"])
			params, _ := op["parameters"].([]any)
			for _, rawParam := range params {
				paramMap, ok := rawParam.(map[string]any)
				if !ok {
					continue
				}
				c.walkParam(operationID, c.resolveAny(paramMap))
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

// walkContent walks every media type's schema under a resolved response or
// requestBody node's `content` map. Shared by Compute's response and request
// body loops: a response object and a resolved requestBody object are both
// shaped `{content: {mediaType: {schema: ...}}}`.
func (c *collector) walkContent(node map[string]any) {
	content, _ := node["content"].(map[string]any)
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

// walkParam records one resolved parameter object (already $ref-followed by
// the caller) as a single Entry keyed by paramKey(operationID, in, name). A
// parameter's own Required/Style/Explode live on the parameter object itself;
// Type/Format/Enum/Nullable/Default and, for arrays, the Item* fields, live
// one level down on its `schema` node. Unlike walk, a parameter's schema is
// never itself filed under its own named Entry — this document's parameter
// schemas are all inline scalars or inline arrays of scalars (confirmed by
// grep: no operation parameter uses an object schema), so there is nothing
// further to recurse into, and ANY future object-shaped parameter schema
// still gets its Type/Enum/Required seen here even without recursion.
//
// Style and Explode are recorded as OpenAPI 3.0's effective values: an unset
// style is `form` for a query or cookie parameter and `simple` for a path or
// header one, and an unset explode is true exactly when the style is `form`.
// Recording the raw keys instead would file an unset explode and an explicit
// `explode: false` as the same entry, though one sends `label=a&label=b` and
// the other `label=a,b`. OpenAPI 3.0 applies style and explode only to a
// parameter described by `schema`: one described by `content` is serialized
// by its media type, so the location default recorded for it is a
// placeholder, not a wire promise (this document has no such parameter).
func (c *collector) walkParam(operationID string, node map[string]any) {
	name := asString(node["name"])
	in := asString(node["in"])
	if name == "" || in == "" {
		// Not a parameter object this document recognizes (for instance, a
		// dangling or unresolved $ref) — nothing to record.
		return
	}

	schema, _ := node["schema"].(map[string]any)
	schema = c.resolveAny(schema)

	style := asString(node["style"])
	if style == "" {
		style = defaultParamStyle(in)
	}
	explode, ok := node["explode"].(bool)
	if !ok {
		explode = style == "form"
	}

	entrySchema, entryMember := paramKey(operationID, in, name)
	entry := Entry{
		Schema:   entrySchema,
		Member:   entryMember,
		Type:     asString(schema["type"]),
		Format:   asString(schema["format"]),
		Enum:     toStringSlice(schema["enum"]),
		Required: asBool(node["required"]),
		Nullable: asBool(schema["nullable"]),
		Style:    style,
		Explode:  explode,
		Default:  defaultString(schema["default"]),
	}

	if entry.Type == "array" {
		if items, ok := schema["items"].(map[string]any); ok {
			resolvedItems := c.resolveAny(items)
			entry.ItemType = asString(resolvedItems["type"])
			entry.ItemFormat = asString(resolvedItems["format"])
			entry.ItemEnum = toStringSlice(resolvedItems["enum"])
			entry.ItemNullable = asBool(resolvedItems["nullable"])
		}
	}

	c.byKey[entry.Schema+"\x00"+entry.Member] = entry
}

// defaultParamStyle is OpenAPI 3.0's `style` for a parameter that sets none:
// `form` in a query or cookie, `simple` in a path or header.
func defaultParamStyle(in string) string {
	switch in {
	case "query", "cookie":
		return "form"
	case "path", "header":
		return "simple"
	}
	return ""
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

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// defaultString renders a schema's `default` value (which, unlike enum
// members, is a single scalar of whatever type the schema declares — string,
// number or bool) as a comparable string, or "" when the schema has none. A
// literal "" default and "no default at all" are indistinguishable here,
// which matches every default this document actually declares (none are the
// empty string).
func defaultString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// walk records every property of the schema node reaches (following $refs and
// flattening allOf and oneOf), then recurses into each property's own object
// or array shape. label names an anonymous (non-$ref) node for entries
// recorded under it; a $ref always overrides it with the component's own
// name.
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

	if merged, ok := c.mergeCombinator(node, "allOf"); ok {
		node = merged
	} else if merged, ok := c.mergeCombinator(node, "oneOf"); ok {
		// oneOf is a union, not an intersection — merging its branches'
		// properties together is not semantically a "oneOf" anymore. This
		// document uses oneOf nowhere today (confirmed by grep), so this
		// branch is defensive only: a best-effort shape so a future oneOf is
		// SEEN by the digest rather than silently invisible, not a claim
		// that the merge is the right model for real union validation.
		node = merged
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
				Nullable: asBool(resolved["nullable"]),
			}

			var itemsNode, addlPropsNode map[string]any
			switch entry.Type {
			case "array":
				if items, ok := resolved["items"].(map[string]any); ok {
					resolvedItems := c.resolveAny(items)
					entry.ItemType = asString(resolvedItems["type"])
					entry.ItemFormat = asString(resolvedItems["format"])
					entry.ItemEnum = toStringSlice(resolvedItems["enum"])
					entry.ItemNullable = asBool(resolvedItems["nullable"])
					itemsNode = items
				}
			case "object":
				if ap, ok := resolved["additionalProperties"].(map[string]any); ok {
					resolvedAP := c.resolveAny(ap)
					entry.AdditionalPropsType = asString(resolvedAP["type"])
					entry.AdditionalPropsFormat = asString(resolvedAP["format"])
					addlPropsNode = ap
				}
			}

			c.byKey[entry.Schema+"\x00"+entry.Member] = entry

			switch entry.Type {
			case "object":
				c.walk(propMap, name+"."+member)
				if addlPropsNode != nil {
					// A map value that is itself a named/object schema gets
					// its own members walked too, under a label distinct
					// from an array's "[]" so the two can never collide.
					c.walk(addlPropsNode, name+"."+member+"{}")
				}
			case "array":
				if itemsNode != nil {
					c.walk(itemsNode, name+"."+member+"[]")
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

// mergeCombinator flattens node[key] (allOf or oneOf: a list of subschemas)
// into a synthetic object node with merged properties and required lists, the
// way the pre-existing allOf handling always worked. It reports ok == false
// when node has no such key, leaving node untouched.
func (c *collector) mergeCombinator(node map[string]any, key string) (map[string]any, bool) {
	list, ok := node[key].([]any)
	if !ok {
		return nil, false
	}
	mergedProps := map[string]any{}
	var mergedRequired []any
	for _, rawSub := range list {
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
	return map[string]any{
		"type":       "object",
		"properties": mergedProps,
		"required":   mergedRequired,
	}, true
}
