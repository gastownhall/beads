package dolt

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gastownhall/beads/internal/config"
	"github.com/gastownhall/beads/internal/storage"
)

// loadMetadataSchema reads the metadata validation config from YAML and
// returns a parsed schema. Returns mode "none" with empty fields if config
// is not initialized, mode is empty/unknown, or no fields are defined.
func loadMetadataSchema() storage.MetadataSchemaConfig {
	mode := config.MetadataValidationMode()
	if mode == "none" {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	rawFields := config.MetadataSchemaFields()
	if rawFields == nil {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	fields := make(map[string]storage.MetadataFieldSchema)
	for name, raw := range rawFields {
		fieldMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		schema := parseFieldSchema(fieldMap)
		fields[name] = schema
	}

	if len(fields) == 0 {
		return storage.MetadataSchemaConfig{Mode: "none"}
	}

	return storage.MetadataSchemaConfig{
		Mode:   mode,
		Fields: fields,
	}
}

// parseFieldSchema converts a raw config map into a MetadataFieldSchema.
func parseFieldSchema(m map[string]interface{}) storage.MetadataFieldSchema {
	schema := storage.MetadataFieldSchema{}

	if t, ok := m["type"].(string); ok {
		schema.Type = storage.MetadataFieldType(t)
	}

	if req, ok := m["required"].(bool); ok {
		schema.Required = req
	}

	// Parse enum values
	if vals, ok := m["values"]; ok {
		switch v := vals.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					schema.Values = append(schema.Values, s)
				}
			}
		case string:
			// Comma-separated fallback
			for _, s := range strings.Split(v, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					schema.Values = append(schema.Values, s)
				}
			}
		}
	}

	// Parse min/max for numeric types
	if min, ok := toFloat64(m["min"]); ok {
		schema.Min = &min
	}
	if max, ok := toFloat64(m["max"]); ok {
		schema.Max = &max
	}

	return schema
}

// toFloat64 converts an interface{} to float64, handling int and float YAML values.
func toFloat64(v interface{}) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// validateMetadataIfConfigured checks metadata against the schema from config.
// In "warn" mode, prints warnings to stderr and returns nil.
// In "error" mode, returns the first validation error.
// In "none" mode (or if config is not initialized), does nothing.
func validateMetadataIfConfigured(metadata json.RawMessage) error {
	schema := loadMetadataSchema()
	if schema.Mode == "none" {
		return nil
	}

	errs := storage.ValidateMetadataSchema(metadata, schema)
	if len(errs) == 0 {
		return nil
	}

	if schema.Mode == "warn" {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "warning: %s\n", e.Error())
		}
		return nil
	}

	// mode == "error"
	return fmt.Errorf("metadata schema violation: %s", errs[0].Error())
}
