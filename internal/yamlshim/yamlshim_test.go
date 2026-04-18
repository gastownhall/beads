package yamlshim

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnmarshalMap(t *testing.T) {
	input := `
name: beads
version: 1
enabled: true
debug: false
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "beads" {
		t.Errorf("name = %v, want beads", m["name"])
	}
	if m["version"] != 1 {
		t.Errorf("version = %v, want 1", m["version"])
	}
	if m["enabled"] != true {
		t.Errorf("enabled = %v, want true", m["enabled"])
	}
	if m["debug"] != false {
		t.Errorf("debug = %v, want false", m["debug"])
	}
}

func TestUnmarshalNestedMap(t *testing.T) {
	input := `
dolt:
  auto-commit: "on"
routing:
  mode: contributor
  default: "."
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	dolt, ok := m["dolt"].(map[string]interface{})
	if !ok {
		t.Fatal("dolt is not a map")
	}
	if dolt["auto-commit"] != "on" {
		t.Errorf("dolt.auto-commit = %v, want on", dolt["auto-commit"])
	}
	routing, ok := m["routing"].(map[string]interface{})
	if !ok {
		t.Fatal("routing is not a map")
	}
	if routing["mode"] != "contributor" {
		t.Errorf("routing.mode = %v", routing["mode"])
	}
}

func TestUnmarshalList(t *testing.T) {
	input := `
repos:
  primary: "."
  additional:
    - "/home/user/project1"
    - "/home/user/project2"
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	repos, ok := m["repos"].(map[string]interface{})
	if !ok {
		t.Fatal("repos is not a map")
	}
	additional, ok := repos["additional"].([]interface{})
	if !ok {
		t.Fatalf("additional is not a list, got %T", repos["additional"])
	}
	if len(additional) != 2 {
		t.Fatalf("additional has %d items, want 2", len(additional))
	}
	if additional[0] != "/home/user/project1" {
		t.Errorf("additional[0] = %v", additional[0])
	}
}

func TestUnmarshalFlowSequence(t *testing.T) {
	input := `tags: [alpha, beta, gamma]`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	tags, ok := m["tags"].([]interface{})
	if !ok {
		t.Fatalf("tags is not a list, got %T", m["tags"])
	}
	if len(tags) != 3 {
		t.Fatalf("tags has %d items, want 3", len(tags))
	}
	if tags[0] != "alpha" {
		t.Errorf("tags[0] = %v", tags[0])
	}
}

func TestUnmarshalFlowMapping(t *testing.T) {
	input := `labels: {area: backend, priority: high}`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	labels, ok := m["labels"].(map[string]interface{})
	if !ok {
		t.Fatalf("labels is not a map, got %T", m["labels"])
	}
	if labels["area"] != "backend" {
		t.Errorf("area = %v", labels["area"])
	}
}

func TestUnmarshalEmptyFlowSequence(t *testing.T) {
	input := `items: []`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	items, ok := m["items"].([]interface{})
	if !ok {
		t.Fatalf("items is not a list, got %T", m["items"])
	}
	if len(items) != 0 {
		t.Errorf("items has %d items, want 0", len(items))
	}
}

func TestUnmarshalStruct(t *testing.T) {
	input := `
sync-branch: main
no-db: true
prefer-dolt: false
`
	type localConfig struct {
		SyncBranch string `yaml:"sync-branch"`
		NoDb       bool   `yaml:"no-db"`
		PreferDolt bool   `yaml:"prefer-dolt"`
	}
	var cfg localConfig
	if err := Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SyncBranch != "main" {
		t.Errorf("SyncBranch = %v, want main", cfg.SyncBranch)
	}
	if !cfg.NoDb {
		t.Error("NoDb should be true")
	}
	if cfg.PreferDolt {
		t.Error("PreferDolt should be false")
	}
}

func TestUnmarshalNestedStruct(t *testing.T) {
	input := `
types:
  custom:
    - wisp
    - spike
`
	type config struct {
		Types struct {
			Custom []string `yaml:"custom"`
		} `yaml:"types"`
	}
	var cfg config
	if err := Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Types.Custom) != 2 {
		t.Fatalf("got %d custom types, want 2", len(cfg.Types.Custom))
	}
	if cfg.Types.Custom[0] != "wisp" {
		t.Errorf("custom[0] = %v", cfg.Types.Custom[0])
	}
}

func TestUnmarshalComments(t *testing.T) {
	input := `
# top-level comment
name: test # inline comment
# another comment
port: 8080
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "test" {
		t.Errorf("name = %v", m["name"])
	}
	if m["port"] != 8080 {
		t.Errorf("port = %v", m["port"])
	}
}

func TestUnmarshalQuotedStrings(t *testing.T) {
	input := `
double: "hello world"
single: 'hello world'
number_string: "42"
bool_string: "true"
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	if m["double"] != "hello world" {
		t.Errorf("double = %v", m["double"])
	}
	if m["single"] != "hello world" {
		t.Errorf("single = %v", m["single"])
	}
	if m["number_string"] != "42" {
		t.Errorf("number_string = %v (%T)", m["number_string"], m["number_string"])
	}
	if m["bool_string"] != "true" {
		t.Errorf("bool_string = %v (%T)", m["bool_string"], m["bool_string"])
	}
}

func TestMarshalMap(t *testing.T) {
	m := map[string]interface{}{
		"name":    "beads",
		"version": 1,
		"enabled": true,
	}
	data, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "name: beads") {
		t.Errorf("missing name, got: %s", s)
	}
	if !strings.Contains(s, "enabled: true") {
		t.Errorf("missing enabled, got: %s", s)
	}
}

func TestMarshalNestedMap(t *testing.T) {
	m := map[string]interface{}{
		"repos": map[string]interface{}{
			"primary": ".",
		},
	}
	data, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "repos:") {
		t.Errorf("missing repos key, got: %s", s)
	}
	if !strings.Contains(s, "primary: .") {
		t.Errorf("missing primary, got: %s", s)
	}
}

func TestNodeEncode(t *testing.T) {
	root := Node{
		Kind: DocumentNode,
		Content: []*Node{
			{Kind: MappingNode, Content: []*Node{
				{Kind: ScalarNode, Value: "repos"},
				{Kind: MappingNode, Content: []*Node{
					{Kind: ScalarNode, Value: "primary"},
					{Kind: ScalarNode, Value: "/home/user/project", Style: DoubleQuotedStyle},
					{Kind: ScalarNode, Value: "additional"},
					{Kind: SequenceNode, Content: []*Node{
						{Kind: ScalarNode, Value: "/other/path", Style: DoubleQuotedStyle},
					}},
				}},
			}},
		},
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}

	s := buf.String()
	if !strings.Contains(s, "repos:") {
		t.Errorf("missing repos, got: %s", s)
	}
	if !strings.Contains(s, `"/home/user/project"`) {
		t.Errorf("missing quoted primary, got: %s", s)
	}
	if !strings.Contains(s, `"/other/path"`) {
		t.Errorf("missing quoted additional, got: %s", s)
	}
}

func TestUnmarshalNull(t *testing.T) {
	input := `
empty: ~
also_null: null
blank:
`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	if m["empty"] != nil {
		t.Errorf("empty = %v, want nil", m["empty"])
	}
	if m["also_null"] != nil {
		t.Errorf("also_null = %v, want nil", m["also_null"])
	}
}

func TestRoundTrip(t *testing.T) {
	input := map[string]interface{}{
		"name":    "test",
		"count":   42,
		"enabled": true,
		"nested": map[string]interface{}{
			"key": "value",
		},
	}
	data, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]interface{}
	if err := Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output["name"] != "test" {
		t.Errorf("name = %v", output["name"])
	}
	if output["count"] != 42 {
		t.Errorf("count = %v", output["count"])
	}
	nested, ok := output["nested"].(map[string]interface{})
	if !ok {
		t.Fatal("nested is not a map")
	}
	if nested["key"] != "value" {
		t.Errorf("nested.key = %v", nested["key"])
	}
}

func TestUnmarshalEmptyMap(t *testing.T) {
	input := `labels: {}`
	var m map[string]interface{}
	if err := Unmarshal([]byte(input), &m); err != nil {
		t.Fatal(err)
	}
	labels, ok := m["labels"].(map[string]interface{})
	if !ok {
		t.Fatalf("labels is not a map, got %T", m["labels"])
	}
	if len(labels) != 0 {
		t.Errorf("labels has %d items, want 0", len(labels))
	}
}

func TestMarshalEmptySlice(t *testing.T) {
	m := map[string]interface{}{
		"items": []interface{}{},
	}
	data, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "items: []") {
		t.Errorf("got: %s", string(data))
	}
}

func TestUnmarshalNode(t *testing.T) {
	input := `
repos:
  primary: "."
  additional:
    - "/path/a"
`
	var node Node
	if err := Unmarshal([]byte(input), &node); err != nil {
		t.Fatal(err)
	}
	if node.Kind != DocumentNode {
		t.Errorf("kind = %v, want DocumentNode", node.Kind)
	}
	if len(node.Content) == 0 {
		t.Fatal("empty document content")
	}
	mapping := node.Content[0]
	if mapping.Kind != MappingNode {
		t.Errorf("root content kind = %v, want MappingNode", mapping.Kind)
	}
}
