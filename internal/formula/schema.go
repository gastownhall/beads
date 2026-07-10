package formula

//go:generate go run ./cmd/schemagen -types types.go -out schema_gen.go

// PrimitiveDoc describes one exported struct (a "primitive") in this package.
// The slice Primitives in schema_gen.go is generated from types.go and serves
// as the discoverability index surfaced by `bd formula schema`.
type PrimitiveDoc struct {
	// Name is the Go struct name (e.g. "Formula", "LoopSpec").
	Name string `json:"name"`

	// Doc is the struct's leading doc comment, trimmed.
	Doc string `json:"doc,omitempty"`

	// Fields are the struct's exported, JSON-visible fields, in source order.
	Fields []FieldDoc `json:"fields,omitempty"`
}

// FieldDoc describes one exported field of a primitive struct.
// Fields tagged json:"-" are excluded from the schema entirely.
type FieldDoc struct {
	// Name is the Go field name.
	Name string `json:"name"`

	// Type is the Go type rendered as source (e.g. "string", "[]*Step",
	// "map[string]*VarDef").
	Type string `json:"type"`

	// JSONName is the field name used in JSON output. Falls back to Name
	// when the json tag is absent.
	JSONName string `json:"json_name,omitempty"`

	// TOMLName is the field name used in TOML output. Falls back to JSONName
	// when no toml tag is present.
	TOMLName string `json:"toml_name,omitempty"`

	// Required is true when the field has no `,omitempty` option in its json
	// tag. This is a structural signal, not a guarantee the parser enforces
	// presence.
	Required bool `json:"required,omitempty"`

	// Doc is the field's leading doc comment, trimmed.
	Doc string `json:"doc,omitempty"`
}

// PrimitiveByName returns the PrimitiveDoc that matches name, or nil.
// Matching is case-insensitive and ignores underscores/dashes; an exact
// (normalized) match wins over a suffix-stripped match. So "gate" returns
// Gate, "gaterule" returns GateRule, and "loop" or "on_complete" return
// LoopSpec / OnCompleteSpec via the Spec/Rule suffix fallback.
func PrimitiveByName(name string) *PrimitiveDoc {
	if name == "" {
		return nil
	}
	target := normalizePrimitiveName(name)
	for i := range Primitives {
		if normalizePrimitiveName(Primitives[i].Name) == target {
			return &Primitives[i]
		}
	}
	for i := range Primitives {
		if stripPrimitiveSuffix(normalizePrimitiveName(Primitives[i].Name)) == target {
			return &Primitives[i]
		}
	}
	return nil
}

func normalizePrimitiveName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || c == '-' || c == ' ' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

func stripPrimitiveSuffix(s string) string {
	for _, suf := range []string{"spec", "rules", "rule"} {
		if len(s) > len(suf) && s[len(s)-len(suf):] == suf {
			return s[:len(s)-len(suf)]
		}
	}
	return s
}
