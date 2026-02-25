package dolt

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestLoadMetadataSchema_DefaultNone(t *testing.T) {
	// Without config.Initialize(), mode should default to "none"
	schema := loadMetadataSchema()
	if schema.Mode != "none" {
		t.Errorf("expected mode 'none', got %q", schema.Mode)
	}
	if len(schema.Fields) != 0 {
		t.Errorf("expected no fields, got %d", len(schema.Fields))
	}
}

func TestValidateMetadataIfConfigured_NoneMode(t *testing.T) {
	// With no config initialized, should always return nil
	err := validateMetadataIfConfigured(json.RawMessage(`{"anything":"goes"}`))
	if err != nil {
		t.Errorf("expected nil error in none mode, got %v", err)
	}
}

func TestParseFieldSchema(t *testing.T) {
	t.Run("EnumField", func(t *testing.T) {
		raw := map[string]interface{}{
			"type":     "enum",
			"values":   []interface{}{"a", "b", "c"},
			"required": true,
		}
		schema := parseFieldSchema(raw)
		if schema.Type != storage.MetadataFieldEnum {
			t.Errorf("expected type enum, got %q", schema.Type)
		}
		if !schema.Required {
			t.Error("expected required=true")
		}
		if len(schema.Values) != 3 {
			t.Errorf("expected 3 values, got %d", len(schema.Values))
		}
	})

	t.Run("IntFieldWithMinMax", func(t *testing.T) {
		raw := map[string]interface{}{
			"type": "int",
			"min":  0,
			"max":  100,
		}
		schema := parseFieldSchema(raw)
		if schema.Type != storage.MetadataFieldInt {
			t.Errorf("expected type int, got %q", schema.Type)
		}
		if schema.Min == nil || *schema.Min != 0 {
			t.Errorf("expected min=0, got %v", schema.Min)
		}
		if schema.Max == nil || *schema.Max != 100 {
			t.Errorf("expected max=100, got %v", schema.Max)
		}
	})

	t.Run("StringField", func(t *testing.T) {
		raw := map[string]interface{}{
			"type": "string",
		}
		schema := parseFieldSchema(raw)
		if schema.Type != storage.MetadataFieldString {
			t.Errorf("expected type string, got %q", schema.Type)
		}
		if schema.Required {
			t.Error("expected required=false by default")
		}
	})
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name   string
		input  interface{}
		want   float64
		wantOK bool
	}{
		{"float64", float64(3.14), 3.14, true},
		{"int", int(42), 42, true},
		{"int64", int64(99), 99, true},
		{"nil", nil, 0, false},
		{"string", "not a number", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat64(tt.input)
			if ok != tt.wantOK {
				t.Errorf("toFloat64(%v) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
