package wireshape

import (
	"reflect"
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
