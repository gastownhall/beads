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
