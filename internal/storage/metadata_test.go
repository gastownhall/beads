package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeMetadataValue(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    string
		wantErr bool
	}{
		{
			name:    "string input",
			input:   `{"key":"value"}`,
			want:    `{"key":"value"}`,
			wantErr: false,
		},
		{
			name:    "[]byte input",
			input:   []byte(`{"key":"value"}`),
			want:    `{"key":"value"}`,
			wantErr: false,
		},
		{
			name:    "json.RawMessage input",
			input:   json.RawMessage(`{"key":"value"}`),
			want:    `{"key":"value"}`,
			wantErr: false,
		},
		{
			name:    "empty object string",
			input:   `{}`,
			want:    `{}`,
			wantErr: false,
		},
		{
			name:    "empty object []byte",
			input:   []byte(`{}`),
			want:    `{}`,
			wantErr: false,
		},
		{
			name:    "complex JSON",
			input:   `{"files":["foo.go","bar.go"],"tool":"linter@1.0"}`,
			want:    `{"files":["foo.go","bar.go"],"tool":"linter@1.0"}`,
			wantErr: false,
		},
		{
			name:    "invalid JSON string",
			input:   `{invalid}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON []byte",
			input:   []byte(`not json`),
			wantErr: true,
		},
		{
			name:    "invalid JSON json.RawMessage",
			input:   json.RawMessage(`{broken`),
			wantErr: true,
		},
		{
			name:    "unsupported type int",
			input:   123,
			wantErr: true,
		},
		{
			name:    "unsupported type map",
			input:   map[string]string{"key": "value"},
			wantErr: true,
		},
		{
			name:    "unsupported type struct",
			input:   struct{ Key string }{Key: "value"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeMetadataValue(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NormalizeMetadataValue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("NormalizeMetadataValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecodeMetadataObject (GH#6330): numbers decode as json.Number holding the
// stored literal, so integers past 2^53 and decimals keep every digit, both at
// the top level and nested; non-object and malformed input is refused.
func TestDecodeMetadataObject(t *testing.T) {
	t.Parallel()
	m, err := DecodeMetadataObject([]byte(`{` +
		`"big":1546130668394913822,` +
		`"just_over":9007199254740993,` +
		`"boundary":9007199254740992,` +
		`"min":-9223372036854775808,` +
		`"umax":18446744073709551615,` +
		`"small":5,"pi":3.14,"tiny":0.0000001,` +
		`"str":"1546130668394913822",` +
		`"nested":{"ids":[9007199254740993,{"x":1546130668394913822}]}}`))
	if err != nil {
		t.Fatalf("DecodeMetadataObject: %v", err)
	}
	for key, want := range map[string]string{
		"big": "1546130668394913822", "just_over": "9007199254740993",
		"boundary": "9007199254740992", "min": "-9223372036854775808",
		"umax": "18446744073709551615", "small": "5", "pi": "3.14", "tiny": "0.0000001",
	} {
		n, ok := m[key].(json.Number)
		if !ok {
			t.Errorf("m[%q] = %T, want json.Number", key, m[key])
			continue
		}
		if n.String() != want {
			t.Errorf("m[%q] = %s, want %s", key, n, want)
		}
	}
	if s, ok := m["str"].(string); !ok || s != "1546130668394913822" {
		t.Errorf("m[str] = %#v, want the string unchanged", m["str"])
	}
	nested, err := json.Marshal(m["nested"])
	if err != nil {
		t.Fatalf("marshal nested: %v", err)
	}
	if want := `{"ids":[9007199254740993,{"x":1546130668394913822}]}`; string(nested) != want {
		t.Errorf("nested = %s, want %s", nested, want)
	}

	for _, empty := range []string{`{}`, ` { } `} {
		got, err := DecodeMetadataObject([]byte(empty))
		if err != nil || got == nil || len(got) != 0 {
			t.Errorf("DecodeMetadataObject(%q) = (%v, %v), want an empty non-nil map", empty, got, err)
		}
	}
	if got, err := DecodeMetadataObject([]byte(`null`)); err != nil || got != nil {
		t.Errorf("DecodeMetadataObject(null) = (%v, %v), want (nil, nil)", got, err)
	}

	for _, bad := range []string{``, `not-json`, `{"a":1}{"b":2}`, `{"a":1} x`, `{"a":`, `[1,2]`, `"str"`, `42`, `true`} {
		got, err := DecodeMetadataObject([]byte(bad))
		if err == nil {
			t.Errorf("DecodeMetadataObject(%q) = %v, want an error", bad, got)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("DecodeMetadataObject(%q) returned an empty error", bad)
		}
	}
}
