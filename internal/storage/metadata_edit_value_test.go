package storage

import (
	"encoding/json"
	"testing"
)

// TestMetadataEditValuePinsV122Inference pins the shipped v1.2.2 `--set-metadata`
// scalar contract (v1.2.2's cmd/bd/update.go toJSONValue): null/bool/number keep
// their JSON type, and a spelling that is not valid JSON on its own stays a
// string even when it scans as a float. An in-window build stored every value as
// a string; that regression is what this table guards against coming back.
func TestMetadataEditValuePinsV122Inference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"integer", "5", `5`},
		{"negative integer", "-2", `-2`},
		{"trailing zero float", "0.50", `0.50`},
		{"exponent", "1e3", `1e3`},
		{"zero", "0", `0`},
		{"true", "true", `true`},
		{"false", "false", `false`},
		{"null", "null", `null`},
		{"leading zero is not JSON", "05", `"05"`},
		{"unary plus is not JSON", "+5", `"+5"`},
		{"NaN is not JSON", "NaN", `"NaN"`},
		{"version triple", "1.2.3", `"1.2.3"`},
		{"empty", "", `""`},
		{"word", "abc", `"abc"`},
		{"sentence", "hello world", `"hello world"`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := string(MetadataEditValue(tt.input))
			if got != tt.want {
				t.Errorf("MetadataEditValue(%q) = %s, want %s", tt.input, got, tt.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("MetadataEditValue(%q) = %s, which is not valid JSON", tt.input, got)
			}
		})
	}
}

// TestApplyMetadataEditsTypesScalars proves the inference reaches the merged
// blob every caller writes (embedded CLI, proxied CLI and the update executor's
// []string path all funnel through ApplyMetadataEdits).
func TestApplyMetadataEditsTypesScalars(t *testing.T) {
	t.Parallel()
	merged, err := ApplyMetadataEdits(
		json.RawMessage(`{"existing":"yes"}`),
		[]string{"score=99", "tier=gold", "ratio=0.5", "done=true", "cleared=null"},
		nil,
	)
	if err != nil {
		t.Fatalf("ApplyMetadataEdits: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", merged, err)
	}
	if got["score"] != float64(99) {
		t.Errorf("metadata[score] = %#v, want the JSON number 99", got["score"])
	}
	if got["ratio"] != 0.5 {
		t.Errorf("metadata[ratio] = %#v, want the JSON number 0.5", got["ratio"])
	}
	if got["done"] != true {
		t.Errorf("metadata[done] = %#v, want the JSON boolean true", got["done"])
	}
	if v, present := got["cleared"]; !present || v != nil {
		t.Errorf("metadata[cleared] = %#v (present=%v), want JSON null", v, present)
	}
	if got["tier"] != "gold" {
		t.Errorf("metadata[tier] = %#v, want %q", got["tier"], "gold")
	}
	if got["existing"] != "yes" {
		t.Errorf("metadata[existing] = %#v, want %q: pre-existing keys must survive", got["existing"], "yes")
	}
}
