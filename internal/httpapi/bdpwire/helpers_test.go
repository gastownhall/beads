package bdpwire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The tests in this package are pure: no network, no database, no build tag.
// They read the vendored files beneath schema/ relative to the package
// directory, which is where `go test` runs them.

const schemaDir = "schema"

func readSchemaFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(schemaDir, rel))
	if err != nil {
		t.Fatalf("read vendored file: %v", err)
	}
	return data
}

// decodeAny parses data into the generic JSON model with numbers kept as
// their literals, so a canonical comparison never turns 16384 into 1.6384e+04.
func decodeAny(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	return v
}

// loadJSON parses a vendored file into the generic model.
func loadJSON(t *testing.T, rel string) any {
	t.Helper()
	return decodeAny(t, readSchemaFile(t, rel))
}

// loadBundleDefs parses the embedded bundle and returns its `$defs`.
func loadBundleDefs(t *testing.T) map[string]any {
	t.Helper()
	bundle := asMap(t, decodeAny(t, SchemaBundle()), "bundle")
	return asMap(t, bundle["$defs"], "$defs")
}

func asMap(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: want object, got %T", what, v)
	}
	return m
}

func asSlice(t *testing.T, v any, what string) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: want array, got %T", what, v)
	}
	return s
}

func asString(t *testing.T, v any, what string) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s: want string, got %T", what, v)
	}
	return s
}

// canonical re-encodes a JSON document so that two documents with the same
// members and values compare equal as bytes: object keys sorted, whitespace
// dropped, string escaping normalized, numbers kept as written.
func canonical(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := json.Marshal(decodeAny(t, data))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	return out
}

// selectAll walks a JSON-pointer-like path over the generic model and returns
// every value it reaches. "*" matches every element of an array and every
// member of an object (in key order); a name or index that is absent matches
// nothing rather than failing, so callers assert on the count.
func selectAll(doc any, path string) []any {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return []any{doc}
	}
	current := []any{doc}
	for _, token := range strings.Split(path, "/") {
		var next []any
		for _, node := range current {
			switch n := node.(type) {
			case map[string]any:
				if token == "*" {
					keys := make([]string, 0, len(n))
					for k := range n {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						next = append(next, n[k])
					}
				} else if v, ok := n[token]; ok {
					next = append(next, v)
				}
			case []any:
				if token == "*" {
					next = append(next, n...)
				} else if i, err := strconv.Atoi(token); err == nil && i >= 0 && i < len(n) {
					next = append(next, n[i])
				}
			}
		}
		current = next
	}
	return current
}

// sortedKeys returns the keys of a set in order, for readable failures.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// diff returns the keys of a that b lacks, sorted.
func diff[V any, W any](a map[string]V, b map[string]W) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
