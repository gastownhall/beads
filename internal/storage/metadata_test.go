package storage

import (
	"encoding/json"
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

// TestDecodeMetadataObject (bd-emny2t, GH#6330) pins the decoder the metadata
// consumers use: numbers come back as json.Number with their stored literal, an
// integer past 2^53 keeps every digit, and the shape rules hold — whole
// document, object root, null/empty as a nil map.
func TestDecodeMetadataObject(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       map[string]string // key -> json.Number literal (numbers only)
		wantString map[string]string // key -> plain string value
		wantNil    bool
		wantErr    bool
	}{
		{
			name:  "large integer keeps literal",
			input: `{"big":1546130668394913822,"just_over":9007199254740993}`,
			want:  map[string]string{"big": "1546130668394913822", "just_over": "9007199254740993"},
		},
		{
			name:  "int64 minimum and uint64 maximum",
			input: `{"min":-9223372036854775808,"max":18446744073709551615}`,
			want:  map[string]string{"min": "-9223372036854775808", "max": "18446744073709551615"},
		},
		{
			name:  "2^53 boundary exact both sides",
			input: `{"under":9007199254740991,"over":9007199254740993}`,
			want:  map[string]string{"under": "9007199254740991", "over": "9007199254740993"},
		},
		{
			name:  "decimal and small integer literals",
			input: `{"five":5,"pi":3.14,"tiny":0.0000001}`,
			want:  map[string]string{"five": "5", "pi": "3.14", "tiny": "0.0000001"},
		},
		{
			name:       "numeric-looking string stays a string",
			input:      `{"s":"1546130668394913822"}`,
			wantString: map[string]string{"s": "1546130668394913822"},
		},
		{
			name:  "empty object",
			input: `{}`,
			want:  map[string]string{},
		},
		{name: "null document", input: `null`, wantNil: true},
		{name: "empty document", input: ``, wantNil: true},
		{name: "whitespace document", input: `   `, wantNil: true},
		{name: "trailing content", input: `{"a":1} {"b":2}`, wantErr: true},
		{name: "malformed JSON", input: `{"a":`, wantErr: true},
		{name: "non-object root number", input: `5`, wantErr: true},
		{name: "non-object root array", input: `[1,2]`, wantErr: true},
		{name: "non-object root string", input: `"str"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeMetadataObject([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("DecodeMetadataObject(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("DecodeMetadataObject(%q) = %v, want nil map", tt.input, got)
				}
				return
			}
			if len(got) != len(tt.want)+len(tt.wantString) {
				t.Fatalf("DecodeMetadataObject(%q) = %v, want %d keys", tt.input, got, len(tt.want)+len(tt.wantString))
			}
			for k, literal := range tt.want {
				v, ok := got[k].(json.Number)
				if !ok {
					t.Errorf("DecodeMetadataObject(%q)[%q] = %T (%v), want json.Number", tt.input, k, got[k], got[k])
					continue
				}
				if v.String() != literal {
					t.Errorf("DecodeMetadataObject(%q)[%q] = %s, want literal %s", tt.input, k, v.String(), literal)
				}
			}
			for k, s := range tt.wantString {
				if got[k] != any(s) {
					t.Errorf("DecodeMetadataObject(%q)[%q] = %#v, want string %q", tt.input, k, got[k], s)
				}
			}
		})
	}

	// Recursive numbers: nested objects and arrays decode with json.Number too,
	// so re-encoding (SlotGet, the show formatter's compact JSON) keeps digits.
	nested, err := DecodeMetadataObject([]byte(`{"obj":{"thread_id":1546130668394913822},"arr":[9007199254740993]}`))
	if err != nil {
		t.Fatalf("DecodeMetadataObject(nested): %v", err)
	}
	obj, ok := nested["obj"].(map[string]any)
	if !ok {
		t.Fatalf("nested object decoded as %T, want map[string]any", nested["obj"])
	}
	if n, ok := obj["thread_id"].(json.Number); !ok || n.String() != "1546130668394913822" {
		t.Errorf("nested obj.thread_id = %#v, want json.Number 1546130668394913822", obj["thread_id"])
	}
	arr, ok := nested["arr"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("nested array decoded as %#v, want one element", nested["arr"])
	}
	if n, ok := arr[0].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Errorf("nested arr[0] = %#v, want json.Number 9007199254740993", arr[0])
	}
}
