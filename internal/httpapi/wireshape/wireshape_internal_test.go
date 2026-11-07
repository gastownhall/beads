package wireshape

import (
	"reflect"
	"slices"
	"testing"
)

// paramDoc is a minimal OpenAPI document with one operation, listWidgets,
// whose only parameter is param: a YAML flow mapping, so a case spells its
// whole parameter object on one line. components holds what the $ref cases
// point at.
func paramDoc(param string) []byte {
	return []byte(`openapi: 3.0.3
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - ` + param + `
      responses: {}
components:
  parameters:
    WidgetColor: {name: color, in: query, required: true, schema: {$ref: '#/components/schemas/Color'}}
  schemas:
    Color: {type: string, enum: [red, green]}
    Shade: {type: integer, format: int32, nullable: true}
`)
}

// paramDigest runs computeFrom — the walk Compute runs — over paramDoc(param).
// The document has no response or request body, so every entry is a
// parameter entry.
func paramDigest(t *testing.T, param string) Digest {
	t.Helper()
	d, err := computeFrom(paramDoc(param), 2)
	if err != nil {
		t.Fatalf("computeFrom: %v", err)
	}
	return d
}

// TestWalkParam pins every Entry field walkParam extracts, feeding each
// synthetic parameter object through computeFrom so a $ref'd parameter is
// resolved exactly as Compute resolves it. TestParameterMutationsAreCaught
// cannot see a wrongly extracted field: it mutates golden entries after the
// fact.
func TestWalkParam(t *testing.T) {
	const op = "param:listWidgets"
	for _, tc := range []struct {
		name  string
		param string
		want  []Entry // nil: nothing recorded
	}{
		{"query array relying on the defaults records form and explode",
			`{name: tags, in: query, schema: {type: array, items: {type: string, enum: [b, a]}}}`,
			[]Entry{{Schema: op, Member: "query:tags", Type: "array", ItemType: "string", ItemEnum: []string{"a", "b"}, Style: "form", Explode: true}}},
		{"explicit explode false is recorded",
			`{name: tags, in: query, explode: false, schema: {type: array, items: {type: string}}}`,
			[]Entry{{Schema: op, Member: "query:tags", Type: "array", ItemType: "string", Style: "form"}}},
		{"explicit non-form style defaults explode to false",
			`{name: tags, in: query, style: pipeDelimited, schema: {type: array, items: {type: string}}}`,
			[]Entry{{Schema: op, Member: "query:tags", Type: "array", ItemType: "string", Style: "pipeDelimited"}}},
		{"path parameter defaults to simple",
			`{name: id, in: path, required: true, schema: {type: string}}`,
			[]Entry{{Schema: op, Member: "path:id", Type: "string", Required: true, Style: "simple"}}},
		{"header parameter defaults to simple",
			`{name: X-Trace, in: header, schema: {type: integer}}`,
			[]Entry{{Schema: op, Member: "header:X-Trace", Type: "integer", Style: "simple"}}},
		{"cookie parameter defaults to form",
			`{name: session, in: cookie, schema: {type: string}}`,
			[]Entry{{Schema: op, Member: "cookie:session", Type: "string", Style: "form", Explode: true}}},
		{"format, nullable and default come from the schema",
			`{name: limit, in: query, schema: {type: integer, format: int32, nullable: true, default: 50}}`,
			[]Entry{{Schema: op, Member: "query:limit", Type: "integer", Format: "int32", Nullable: true, Default: "50", Style: "form", Explode: true}}},
		{"a false default is still a default",
			`{name: all, in: query, schema: {type: boolean, default: false}}`,
			[]Entry{{Schema: op, Member: "query:all", Type: "boolean", Default: "false", Style: "form", Explode: true}}},
		{"a $ref'd parameter and its $ref'd schema are both resolved",
			`{$ref: '#/components/parameters/WidgetColor'}`,
			[]Entry{{Schema: op, Member: "query:color", Type: "string", Enum: []string{"green", "red"}, Required: true, Style: "form", Explode: true}}},
		{"$ref'd array items fill the Item fields",
			`{name: shades, in: query, schema: {type: array, items: {$ref: '#/components/schemas/Shade'}}}`,
			[]Entry{{Schema: op, Member: "query:shades", Type: "array", ItemType: "integer", ItemFormat: "int32", ItemNullable: true, Style: "form", Explode: true}}},
		{"a content parameter is recorded only as its container",
			`{name: filter, in: query, content: {application/json: {schema: {type: object}}}}`,
			[]Entry{{Schema: op, Member: "query:filter", Style: "form", Explode: true}}},
		{"missing in records nothing",
			`{name: orphan, schema: {type: string}}`,
			nil},
		{"missing name records nothing",
			`{in: query, schema: {type: string}}`,
			nil},
		{"a dangling $ref records nothing",
			`{$ref: '#/components/parameters/Missing'}`,
			nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := paramDigest(t, tc.param).Entries
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("entries:\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

// sideDoc exercises the three combinations Compute's side-tracking must tell
// apart: a schema reachable ONLY from a request body (reqOnly's ReqOnly), one
// reachable ONLY from a response (respOnly's RespOnly), and one reachable
// from both a request body and a response, on the SAME operation (bothSides'
// Shared) — the case recordSide's "promote to both" path exists for.
const sideDoc = `openapi: 3.0.3
paths:
  /req-only:
    post:
      operationId: reqOnly
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/ReqOnly'}
      responses:
        '200': {description: ok}
  /resp-only:
    get:
      operationId: respOnly
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/RespOnly'}
  /both:
    put:
      operationId: bothSides
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Shared'}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Shared'}
components:
  schemas:
    ReqOnly: {type: object, properties: {tier: {type: string, enum: [a, b]}}}
    RespOnly: {type: object, properties: {tier: {type: string, enum: [a, b]}}}
    Shared: {type: object, properties: {tier: {type: string, enum: [a, b]}}}
`

// TestSideTracking pins Compute's own Digest.Sides assignment (what
// widensAdditively's oldSide/newSide lookups rely on): a request-only schema
// is "request", a response-only schema is "response", and a schema reached
// from both — even when the already-visited path skips re-walking its
// properties — is promoted to "both", never stuck at whichever side happened
// to walk it first.
func TestSideTracking(t *testing.T) {
	d, err := computeFrom([]byte(sideDoc), 2)
	if err != nil {
		t.Fatalf("computeFrom: %v", err)
	}

	for schema, wantSide := range map[string]string{
		"ReqOnly":  "request",
		"RespOnly": "response",
		"Shared":   "both",
	} {
		if got := d.Sides[schema]; got != wantSide {
			t.Errorf("Sides[%q] = %q, want %q", schema, got, wantSide)
		}
	}
}

// TestEffectiveSerializationIsCompared is the Compute-level falsification for
// recording effective style/explode values: changing how a parameter that
// relied on its defaults is serialized must be a changed entry SafeToWrite
// refuses at the same revision, while spelling those same defaults out must
// change nothing.
func TestEffectiveSerializationIsCompared(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after string
		wantChanged   bool
	}{
		{"explode false on a query array that relied on form's implicit explode",
			`{name: tags, in: query, schema: {type: array, items: {type: string}}}`,
			`{name: tags, in: query, explode: false, schema: {type: array, items: {type: string}}}`,
			true},
		{"a non-form style on a query array that relied on form",
			`{name: tags, in: query, schema: {type: array, items: {type: string}}}`,
			`{name: tags, in: query, style: spaceDelimited, schema: {type: array, items: {type: string}}}`,
			true},
		{"label style on a path parameter that relied on simple",
			`{name: id, in: path, required: true, schema: {type: string}}`,
			`{name: id, in: path, required: true, style: label, schema: {type: string}}`,
			true},
		{"spelling out a query parameter's default style and explode",
			`{name: tags, in: query, schema: {type: array, items: {type: string}}}`,
			`{name: tags, in: query, style: form, explode: true, schema: {type: array, items: {type: string}}}`,
			false},
		{"spelling out a path parameter's default style and explode",
			`{name: id, in: path, required: true, schema: {type: string}}`,
			`{name: id, in: path, required: true, style: simple, explode: false, schema: {type: string}}`,
			false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := paramDigest(t, tc.before), paramDigest(t, tc.after)
			cmp := Compare(before, after)
			if len(cmp.Removed) > 0 || len(cmp.Added) > 0 {
				t.Fatalf("parameter key moved: removed=%v added=%v", cmp.Removed, cmp.Added)
			}
			if changed := len(cmp.Changed) > 0; changed != tc.wantChanged {
				t.Errorf("Compare changed=%v, want a change: %v", cmp.Changed, tc.wantChanged)
			}
			if ok, _ := SafeToWrite(before, after); ok == tc.wantChanged {
				t.Errorf("SafeToWrite at the same revision = %v, want %v", ok, !tc.wantChanged)
			}
		})
	}
}

// sharedNestedDoc reproduces the shape the MED1 re-review flagged: Parent is
// a request body (createParent) AND reachable from a response through
// Wrapper.p (getWrapper) — the same "reached from both sides" case sideDoc's
// Shared covers at the top level, but with two further wrinkles sideDoc
// cannot: Child is a NAMED schema nested two levels down (Parent.child,
// $ref'd), and Parent.inl is an ANONYMOUS nested object (no $ref of its own,
// so it is only ever identified by its synthetic "Parent.inl" label) with its
// own enum member. Both sit below the point where the old walk's name-only
// (not name+side) guard stopped recursing on Parent's SECOND visit, so
// recordSide was never called on them for whichever side arrived second —
// and which side that was depended on paths' map iteration order, which is
// why the bug showed up as a flaky 78/100 rather than a deterministic
// failure.
func sharedNestedDoc(kindEnum, modeEnum string) []byte {
	return []byte(`openapi: 3.0.3
paths:
  /parent:
    post:
      operationId: createParent
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Parent'}
      responses:
        '200': {description: ok}
  /wrapper:
    get:
      operationId: getWrapper
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Wrapper'}
components:
  schemas:
    Parent:
      type: object
      properties:
        child: {$ref: '#/components/schemas/Child'}
        inl:
          type: object
          properties:
            mode: {type: string, enum: [` + modeEnum + `]}
    Child:
      type: object
      properties:
        kind: {type: string, enum: [` + kindEnum + `]}
    Wrapper:
      type: object
      properties:
        p: {$ref: '#/components/schemas/Parent'}
`)
}

// TestSidePropagatesToNestedChildren pins the MED1 re-review fix directly:
// Parent is reachable from both createParent's request body and, through
// Wrapper.p, getWrapper's response, and "both" must reach all the way down
// to its children — Child (a named, $ref'd grandchild) and Parent.inl (an
// anonymous nested object) alike — not stop at Parent itself. Before the
// fix, whichever side's walk reached Parent SECOND found it already in the
// name-only visited set and returned before ever recursing into Parent's
// properties with that side, so Child and Parent.inl stayed stuck at
// whichever single side walked them first.
func TestSidePropagatesToNestedChildren(t *testing.T) {
	d, err := computeFrom(sharedNestedDoc("a, b", "a, b"), 2)
	if err != nil {
		t.Fatalf("computeFrom: %v", err)
	}
	want := map[string]string{
		"Parent":     "both",
		"Child":      "both",
		"Parent.inl": "both",
		"Wrapper":    "response",
	}
	for schema, wantSide := range want {
		if got := d.Sides[schema]; got != wantSide {
			t.Errorf("Sides[%q] = %q, want %q", schema, got, wantSide)
		}
	}
}

// TestEnumWideningOnSharedNestedChildIsNotAdditive is the end-to-end
// Compare-level falsification the re-review asked for: widening an enum on a
// schema reachable from BOTH sides — whether a named grandchild ($ref'd
// Child.kind) or an anonymous nested one (Parent.inl.mode) — must be
// classified as a breaking Changed entry, never as an additive Widened one.
// An existing client decoding Wrapper out of getWrapper's response has to
// keep recognizing every value the server might send on either member, so
// neither widening is something only an updated client opts into the way a
// request-only widening is.
func TestEnumWideningOnSharedNestedChildIsNotAdditive(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		beforeKind, afterKind string
		beforeMode, afterMode string
		wantKey               string
	}{
		{"named grandchild (Child.kind)", "a, b", "a, b, c", "a, b", "a, b", "Child\x00kind"},
		{"anonymous nested child (Parent.inl.mode)", "a, b", "a, b", "a, b", "a, b, c", "Parent.inl\x00mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := computeFrom(sharedNestedDoc(tc.beforeKind, tc.beforeMode), 2)
			if err != nil {
				t.Fatalf("computeFrom(before): %v", err)
			}
			after, err := computeFrom(sharedNestedDoc(tc.afterKind, tc.afterMode), 2)
			if err != nil {
				t.Fatalf("computeFrom(after): %v", err)
			}

			cmp := Compare(before, after)
			if !slices.Contains(cmp.Changed, tc.wantKey) {
				t.Errorf("Changed = %v, want it to include %q: a both-sides enum widening must break", cmp.Changed, tc.wantKey)
			}
			if slices.Contains(cmp.Widened, tc.wantKey) {
				t.Errorf("Widened = %v, must NOT include %q: both sides are reachable, so this is not additive", cmp.Widened, tc.wantKey)
			}

			if ok, _ := SafeToWrite(before, after); ok {
				t.Errorf("SafeToWrite at the same revision = true, want false: this widening is breaking and needs a wire_revision bump")
			}
			bumped := Digest{WireRevision: before.WireRevision + 1, Entries: after.Entries, Sides: after.Sides}
			if ok, reason := SafeToWrite(before, bumped); !ok {
				t.Errorf("SafeToWrite with a bumped revision refused: %s", reason)
			}
		})
	}
}

// TestComputeFromSideAssignmentIsDeterministic is the order-independence pin
// the re-review asked for: the bug this MED1 fix closes was map-order
// dependent (observed failing 78/100 runs), so a single passing run of
// TestSidePropagatesToNestedChildren does not rule out a regression that is
// merely less likely than before. Go randomizes map iteration order per
// range, including paths and properties here, so calling computeFrom many
// times over the SAME document input exercises many different traversal
// orders; every one of them must land on the identical Sides assignment and
// entry set.
func TestComputeFromSideAssignmentIsDeterministic(t *testing.T) {
	doc := sharedNestedDoc("a, b", "a, b")
	first, err := computeFrom(doc, 2)
	if err != nil {
		t.Fatalf("computeFrom: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := computeFrom(doc, 2)
		if err != nil {
			t.Fatalf("computeFrom (run %d): %v", i, err)
		}
		if !reflect.DeepEqual(got.Sides, first.Sides) {
			t.Fatalf("run %d: Sides = %v, want %v (non-deterministic across map iteration orders)", i, got.Sides, first.Sides)
		}
		if !reflect.DeepEqual(got.Entries, first.Entries) {
			t.Fatalf("run %d: Entries = %v, want %v (non-deterministic across map iteration orders)", i, got.Entries, first.Entries)
		}
	}
}
