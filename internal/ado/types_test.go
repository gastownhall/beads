package ado

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestSecretString_String(t *testing.T) {
	s := NewSecretString("super-secret-token")
	if got := s.String(); got != "[REDACTED]" {
		t.Errorf("String() = %q, want %q", got, "[REDACTED]")
	}
}

func TestSecretString_Expose(t *testing.T) {
	const want = "super-secret-token"
	s := NewSecretString(want)
	if got := s.Expose(); got != want {
		t.Errorf("Expose() = %q, want %q", got, want)
	}
}

func TestSecretString_MarshalJSON(t *testing.T) {
	s := NewSecretString("super-secret-token")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("MarshalJSON() unexpected error: %v", err)
	}
	if got := string(b); got != `"[REDACTED]"` {
		t.Errorf("MarshalJSON() = %s, want %s", got, `"[REDACTED]"`)
	}
}

func TestSecretString_InFmtSprintf(t *testing.T) {
	s := NewSecretString("super-secret-token")

	tests := []struct {
		name   string
		format string
	}{
		{"percent_s", "%s"},
		{"percent_v", "%v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmt.Sprintf(tt.format, s)
			if got != "[REDACTED]" {
				t.Errorf("Sprintf(%q, secret) = %q, want %q", tt.format, got, "[REDACTED]")
			}
		})
	}
}

func TestSecretString_InStructJSON(t *testing.T) {
	type config struct {
		Token SecretString `json:"token"`
		URL   string       `json:"url"`
	}
	c := config{
		Token: NewSecretString("my-pat"),
		URL:   "https://dev.azure.com",
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("json.Marshal() unexpected error: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal() unexpected error: %v", err)
	}
	if tok, ok := m["token"].(string); !ok || tok != "[REDACTED]" {
		t.Errorf("token field = %v, want %q", m["token"], "[REDACTED]")
	}
	if u, ok := m["url"].(string); !ok || u != "https://dev.azure.com" {
		t.Errorf("url field = %v, want %q", m["url"], "https://dev.azure.com")
	}
}

func TestSecretString_IsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", true},
		{"non_empty", "abc", false},
		{"whitespace_only", " ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSecretString(tt.value)
			if got := s.IsEmpty(); got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkItem_GetField(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]interface{}
		key    string
		want   interface{}
	}{
		{"nil_fields", nil, "System.Title", nil},
		{"missing_key", map[string]interface{}{"System.Title": "hello"}, "System.State", nil},
		{"existing_key", map[string]interface{}{"System.Title": "hello"}, "System.Title", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkItem{Fields: tt.fields}
			got := w.GetField(tt.key)
			if got != tt.want {
				t.Errorf("GetField(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestWorkItem_GetStringField(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]interface{}
		key    string
		want   string
	}{
		{"string_field", map[string]interface{}{"System.Title": "Fix bug"}, "System.Title", "Fix bug"},
		{"non_string_field", map[string]interface{}{"System.Title": 42}, "System.Title", ""},
		{"missing_field", map[string]interface{}{}, "System.Title", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkItem{Fields: tt.fields}
			if got := w.GetStringField(tt.key); got != tt.want {
				t.Errorf("GetStringField(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestWorkItem_GetIntField(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]interface{}
		key    string
		want   int
	}{
		{"int_field", map[string]interface{}{"p": 2}, "p", 2},
		{"float64_field", map[string]interface{}{"p": float64(3)}, "p", 3},
		{"non_numeric_field", map[string]interface{}{"p": "abc"}, "p", 0},
		{"missing_field", map[string]interface{}{}, "p", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkItem{Fields: tt.fields}
			if got := w.GetIntField(tt.key); got != tt.want {
				t.Errorf("GetIntField(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

func TestWorkItem_JSONRoundTrip(t *testing.T) {
	original := WorkItem{
		ID:  42,
		Rev: 3,
		Fields: map[string]interface{}{
			FieldTitle: "Test item",
			FieldState: "Active",
		},
		URL: "https://dev.azure.com/org/project/_apis/wit/workItems/42",
		Relations: []WorkItemRelation{
			{
				Rel: RelParent,
				URL: "https://dev.azure.com/org/project/_apis/wit/workItems/10",
				Attributes: map[string]interface{}{
					"name": "Parent",
				},
			},
		},
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	var decoded WorkItem
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %d, want %d", decoded.ID, original.ID)
	}
	if decoded.Rev != original.Rev {
		t.Errorf("Rev = %d, want %d", decoded.Rev, original.Rev)
	}
	if decoded.URL != original.URL {
		t.Errorf("URL = %q, want %q", decoded.URL, original.URL)
	}
	if decoded.GetStringField(FieldTitle) != "Test item" {
		t.Errorf("Fields[Title] = %q, want %q", decoded.GetStringField(FieldTitle), "Test item")
	}
	if decoded.GetStringField(FieldState) != "Active" {
		t.Errorf("Fields[State] = %q, want %q", decoded.GetStringField(FieldState), "Active")
	}
	if len(decoded.Relations) != 1 {
		t.Fatalf("len(Relations) = %d, want 1", len(decoded.Relations))
	}
	if decoded.Relations[0].Rel != RelParent {
		t.Errorf("Relations[0].Rel = %q, want %q", decoded.Relations[0].Rel, RelParent)
	}
}

func TestWIQLResult_JSONParsing(t *testing.T) {
	raw := `{
		"workItems": [
			{"id": 1, "url": "https://dev.azure.com/org/project/_apis/wit/workItems/1"},
			{"id": 2, "url": "https://dev.azure.com/org/project/_apis/wit/workItems/2"},
			{"id": 3, "url": "https://dev.azure.com/org/project/_apis/wit/workItems/3"}
		]
	}`

	var result WIQLResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if len(result.WorkItems) != 3 {
		t.Fatalf("len(WorkItems) = %d, want 3", len(result.WorkItems))
	}
	for i, ref := range result.WorkItems {
		if ref.ID != i+1 {
			t.Errorf("WorkItems[%d].ID = %d, want %d", i, ref.ID, i+1)
		}
		if ref.URL == "" {
			t.Errorf("WorkItems[%d].URL is empty", i)
		}
	}
}

func TestProject_JSONParsing(t *testing.T) {
	raw := `{
		"id": "6ce954b1-ce1f-45d1-b94d-e6bf2464ba2c",
		"name": "MyProject",
		"description": "A sample project",
		"url": "https://dev.azure.com/org/_apis/projects/6ce954b1",
		"state": "wellFormed"
	}`

	var p Project
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	if p.ID != "6ce954b1-ce1f-45d1-b94d-e6bf2464ba2c" {
		t.Errorf("ID = %q, want %q", p.ID, "6ce954b1-ce1f-45d1-b94d-e6bf2464ba2c")
	}
	if p.Name != "MyProject" {
		t.Errorf("Name = %q, want %q", p.Name, "MyProject")
	}
	if p.Description != "A sample project" {
		t.Errorf("Description = %q, want %q", p.Description, "A sample project")
	}
	if p.State != "wellFormed" {
		t.Errorf("State = %q, want %q", p.State, "wellFormed")
	}
}

func TestPatchOperation_JSONMarshal(t *testing.T) {
	tests := []struct {
		name string
		op   PatchOperation
		want string
	}{
		{
			name: "add_field",
			op:   PatchOperation{Op: "add", Path: "/fields/System.Title", Value: "New title"},
			want: `{"op":"add","path":"/fields/System.Title","value":"New title"}`,
		},
		{
			name: "replace_field",
			op:   PatchOperation{Op: "replace", Path: "/fields/System.State", Value: "Active"},
			want: `{"op":"replace","path":"/fields/System.State","value":"Active"}`,
		},
		{
			name: "remove_field",
			op:   PatchOperation{Op: "remove", Path: "/fields/System.Tags"},
			want: `{"op":"remove","path":"/fields/System.Tags"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.op)
			if err != nil {
				t.Fatalf("json.Marshal() error: %v", err)
			}
			if got := string(b); got != tt.want {
				t.Errorf("json.Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}
