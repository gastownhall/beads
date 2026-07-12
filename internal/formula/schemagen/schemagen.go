// Package schemagen walks an internal/formula types.go file and produces
// the body of internal/formula/schema_gen.go (a `var Primitives` index of
// every exported struct). The package is consumed both by the standalone
// generator binary in cmd/schemagen and by the TestSchemaGenIsCurrent test
// in internal/formula.
package schemagen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// Primitive is the in-memory representation of one PrimitiveDoc. Kept
// distinct from formula.PrimitiveDoc to avoid an import cycle: this package
// generates the source that defines formula.Primitives.
type Primitive struct {
	Name   string
	Doc    string
	Fields []Field
}

// Field is the in-memory representation of one FieldDoc.
type Field struct {
	Name     string
	Type     string
	JSONName string
	TOMLName string
	Required bool
	Doc      string
}

// Generate parses typesPath and returns gofmt'd source for schema_gen.go.
// The output is deterministic given the same input.
func Generate(typesPath string) ([]byte, error) {
	prims, err := Parse(typesPath)
	if err != nil {
		return nil, err
	}
	return Render(prims)
}

// Parse extracts every exported struct in typesPath, sorted by name.
func Parse(typesPath string) ([]Primitive, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, typesPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", typesPath, err)
	}

	var prims []Primitive
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if !ts.Name.IsExported() {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			doc := extractDoc(ts.Doc, gen.Doc)
			fields := collectFields(fset, st)
			prims = append(prims, Primitive{
				Name:   ts.Name.Name,
				Doc:    doc,
				Fields: fields,
			})
		}
	}

	sort.Slice(prims, func(i, j int) bool {
		return prims[i].Name < prims[j].Name
	})
	return prims, nil
}

func collectFields(fset *token.FileSet, st *ast.StructType) []Field {
	if st.Fields == nil {
		return nil
	}
	var fields []Field
	for _, f := range st.Fields.List {
		jsonTag, tomlTag := extractTags(f.Tag)
		// Skip fields explicitly excluded from JSON serialization — these
		// are internal implementation details (e.g. Step.SourceFormula).
		if jsonTag.name == "-" {
			continue
		}
		typeStr := renderType(fset, f.Type)
		doc := extractDoc(f.Doc, nil)
		for _, name := range f.Names {
			if !name.IsExported() {
				continue
			}
			jsonName := jsonTag.name
			if jsonName == "" {
				jsonName = name.Name
			}
			tomlName := tomlTag.name
			if tomlName == "" {
				tomlName = jsonName
			}
			required := !jsonTag.omitempty && jsonTag.name != "-"
			fields = append(fields, Field{
				Name:     name.Name,
				Type:     typeStr,
				JSONName: jsonName,
				TOMLName: tomlName,
				Required: required,
				Doc:      doc,
			})
		}
	}
	return fields
}

type structTag struct {
	name      string
	omitempty bool
}

func extractTags(tag *ast.BasicLit) (jsonTag, tomlTag structTag) {
	if tag == nil {
		return
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return
	}
	st := newReflectStructTag(raw)
	if v, ok := st.lookup("json"); ok {
		jsonTag = parseTagValue(v)
	}
	if v, ok := st.lookup("toml"); ok {
		tomlTag = parseTagValue(v)
	}
	return
}

func parseTagValue(v string) structTag {
	parts := strings.Split(v, ",")
	t := structTag{name: parts[0]}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			t.omitempty = true
		}
	}
	return t
}

// reflectStructTag is a tiny stand-in for reflect.StructTag.Lookup that
// avoids depending on reflect at codegen time. Behavior matches the stdlib:
// keys are space-separated, values are double-quoted.
type reflectStructTag string

func newReflectStructTag(s string) reflectStructTag { return reflectStructTag(s) }

func (t reflectStructTag) lookup(key string) (string, bool) {
	tag := string(t)
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			break
		}
		name := tag[:i]
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		quoted := tag[:i+1]
		tag = tag[i+1:]
		if name == key {
			value, err := strconv.Unquote(quoted)
			if err != nil {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

func renderType(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, expr); err != nil {
		return fmt.Sprintf("<unprintable: %v>", err)
	}
	return buf.String()
}

func extractDoc(primary, fallback *ast.CommentGroup) string {
	g := primary
	if g == nil {
		g = fallback
	}
	if g == nil {
		return ""
	}
	return strings.TrimRight(g.Text(), "\n")
}

// Render emits a gofmt'd schema_gen.go body from prims.
func Render(prims []Primitive) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/formula/cmd/schemagen. DO NOT EDIT.\n")
	buf.WriteString("\n")
	buf.WriteString("package formula\n")
	buf.WriteString("\n")
	buf.WriteString("// Primitives is the discoverability index of every exported struct in\n")
	buf.WriteString("// types.go. Surfaced via `bd formula schema`. Regenerate with `go generate ./...`.\n")
	buf.WriteString("var Primitives = []PrimitiveDoc{\n")
	for _, p := range prims {
		buf.WriteString("\t{\n")
		fmt.Fprintf(&buf, "\t\tName: %q,\n", p.Name)
		if p.Doc != "" {
			fmt.Fprintf(&buf, "\t\tDoc:  %s,\n", goStringLit(p.Doc))
		}
		if len(p.Fields) > 0 {
			buf.WriteString("\t\tFields: []FieldDoc{\n")
			for _, f := range p.Fields {
				buf.WriteString("\t\t\t{\n")
				fmt.Fprintf(&buf, "\t\t\t\tName:     %q,\n", f.Name)
				fmt.Fprintf(&buf, "\t\t\t\tType:     %q,\n", f.Type)
				fmt.Fprintf(&buf, "\t\t\t\tJSONName: %q,\n", f.JSONName)
				if f.TOMLName != "" && f.TOMLName != f.JSONName {
					fmt.Fprintf(&buf, "\t\t\t\tTOMLName: %q,\n", f.TOMLName)
				}
				if f.Required {
					buf.WriteString("\t\t\t\tRequired: true,\n")
				}
				if f.Doc != "" {
					fmt.Fprintf(&buf, "\t\t\t\tDoc:      %s,\n", goStringLit(f.Doc))
				}
				buf.WriteString("\t\t\t},\n")
			}
			buf.WriteString("\t\t},\n")
		}
		buf.WriteString("\t},\n")
	}
	buf.WriteString("}\n")
	return format.Source(buf.Bytes())
}

// goStringLit emits a Go string literal — backtick-quoted when the content
// is "clean" (no backticks, no control chars except newline), otherwise
// double-quoted via strconv.Quote so escapes survive.
func goStringLit(s string) string {
	if strings.ContainsRune(s, '`') {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return strconv.Quote(s)
		}
	}
	return "`" + s + "`"
}
