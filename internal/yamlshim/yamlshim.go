// Package yamlshim provides a minimal subset YAML parser and marshaler.
// It handles the YAML constructs used by beads config files: scalars,
// maps, lists, nested structures, and struct tags.
package yamlshim

import (
	"bufio"
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Unmarshal parses YAML data into the value pointed to by v.
// v must be a pointer to a map[string]interface{}, a struct, or *Node.
func Unmarshal(data []byte, v interface{}) error {
	lines := splitLines(data)
	val, _, err := parseValue(lines, 0, 0)
	if err != nil {
		return err
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("yaml: unmarshal requires a non-nil pointer")
	}

	target := rv.Elem()

	if target.Type() == reflect.TypeOf(Node{}) {
		node := valueToNode(val)
		docNode := Node{
			Kind:    DocumentNode,
			Content: []*Node{node},
		}
		target.Set(reflect.ValueOf(docNode))
		return nil
	}

	switch target.Kind() {
	case reflect.Map:
		m, ok := val.(map[string]interface{})
		if !ok {
			if val == nil {
				return nil
			}
			return fmt.Errorf("yaml: cannot unmarshal into map: got %T", val)
		}
		if target.IsNil() {
			target.Set(reflect.MakeMap(target.Type()))
		}
		for k, v := range m {
			target.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(v))
		}
	case reflect.Struct:
		m, ok := val.(map[string]interface{})
		if !ok {
			if val == nil {
				return nil
			}
			return fmt.Errorf("yaml: cannot unmarshal into struct: got %T", val)
		}
		return unmarshalStruct(m, target)
	case reflect.Interface:
		if val != nil {
			target.Set(reflect.ValueOf(val))
		}
	default:
		return fmt.Errorf("yaml: unsupported unmarshal target type: %s", target.Kind())
	}
	return nil
}

// Marshal serializes the value v into YAML bytes.
func Marshal(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := marshalValue(&buf, v, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type line struct {
	indent  int
	content string
	raw     string
}

func splitLines(data []byte) []line {
	var result []line
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		raw := scanner.Text()

		indent := 0
		for _, ch := range raw {
			if ch == ' ' {
				indent++
			} else {
				break
			}
		}

		content := strings.TrimSpace(raw)
		commentIdx := findUnquotedComment(content)
		if commentIdx >= 0 {
			content = strings.TrimRight(content[:commentIdx], " \t")
		}
		if content == "" {
			continue
		}
		result = append(result, line{indent: indent, content: content})
	}
	return result
}

func findUnquotedComment(s string) int {
	inSingle := false
	inDouble := false
	for i, ch := range s {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}

func parseValue(lines []line, start, minIndent int) (interface{}, int, error) {
	if start >= len(lines) {
		return nil, start, nil
	}

	l := lines[start]
	if l.indent < minIndent {
		return nil, start, nil
	}

	if strings.HasPrefix(l.content, "- ") || l.content == "-" {
		return parseList(lines, start, l.indent)
	}

	colonIdx := findUnquotedColon(l.content)
	if colonIdx > 0 {
		return parseMap(lines, start, l.indent)
	}

	return parseScalar(l.content), start + 1, nil
}

func findUnquotedColon(s string) int {
	inSingle := false
	inDouble := false
	for i, ch := range s {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if !inSingle && !inDouble {
				if i+1 >= len(s) || s[i+1] == ' ' {
					return i
				}
			}
		}
	}
	return -1
}

func parseMap(lines []line, start, indent int) (map[string]interface{}, int, error) {
	m := make(map[string]interface{})
	i := start

	for i < len(lines) {
		l := lines[i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			break
		}

		if strings.HasPrefix(l.content, "- ") || l.content == "-" {
			break
		}

		colonIdx := findUnquotedColon(l.content)
		if colonIdx < 0 {
			break
		}

		key := strings.TrimSpace(l.content[:colonIdx])
		key = unquoteScalar(key)
		valStr := strings.TrimSpace(l.content[colonIdx+1:])

		if valStr == "" || valStr == "|" || valStr == ">" {
			if valStr == "|" || valStr == ">" {
				literal := valStr == "|"
				i++
				val, nextI := parseBlock(lines, i, indent+2, literal)
				m[key] = val
				i = nextI
				continue
			}
			i++
			if i < len(lines) && lines[i].indent > indent {
				val, nextI, err := parseValue(lines, i, lines[i].indent)
				if err != nil {
					return nil, i, err
				}
				m[key] = val
				i = nextI
			} else {
				m[key] = nil
			}
		} else if strings.HasPrefix(valStr, "[") {
			val := parseFlowSequence(valStr)
			m[key] = val
			i++
		} else if strings.HasPrefix(valStr, "{") {
			val := parseFlowMapping(valStr)
			m[key] = val
			i++
		} else {
			m[key] = parseScalar(valStr)
			i++
		}
	}
	return m, i, nil
}

func parseBlock(lines []line, start, minIndent int, literal bool) (string, int) {
	var parts []string
	i := start
	for i < len(lines) {
		if lines[i].indent < minIndent {
			break
		}
		parts = append(parts, lines[i].content)
		i++
	}
	sep := " "
	if literal {
		sep = "\n"
	}
	result := strings.Join(parts, sep)
	if literal && len(parts) > 0 {
		result += "\n"
	}
	return result, i
}

func parseList(lines []line, start, indent int) ([]interface{}, int, error) {
	var list []interface{}
	i := start

	for i < len(lines) {
		l := lines[i]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			break
		}

		if !strings.HasPrefix(l.content, "- ") && l.content != "-" {
			break
		}

		var itemContent string
		if l.content == "-" {
			itemContent = ""
		} else {
			itemContent = strings.TrimSpace(l.content[2:])
		}

		if itemContent == "" && i+1 < len(lines) && lines[i+1].indent > indent {
			i++
			val, nextI, err := parseValue(lines, i, lines[i].indent)
			if err != nil {
				return nil, i, err
			}
			list = append(list, val)
			i = nextI
		} else {
			colonIdx := findUnquotedColon(itemContent)
			if colonIdx > 0 && i+1 < len(lines) && lines[i+1].indent > indent {
				subLines := []line{{indent: indent + 2, content: itemContent}}
				j := i + 1
				for j < len(lines) && lines[j].indent > indent {
					subLines = append(subLines, lines[j])
					j++
				}
				val, _, err := parseMap(subLines, 0, indent+2)
				if err != nil {
					return nil, i, err
				}
				list = append(list, val)
				i = j
			} else if colonIdx > 0 {
				val, _, err := parseMap([]line{{indent: 0, content: itemContent}}, 0, 0)
				if err != nil {
					list = append(list, parseScalar(itemContent))
				} else {
					list = append(list, val)
				}
				i++
			} else {
				list = append(list, parseScalar(itemContent))
				i++
			}
		}
	}
	return list, i, nil
}

func parseFlowSequence(s string) []interface{} {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []interface{}{}
	}
	parts := splitFlowItems(inner)
	var result []interface{}
	for _, p := range parts {
		result = append(result, parseScalar(strings.TrimSpace(p)))
	}
	return result
}

func parseFlowMapping(s string) map[string]interface{} {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{})
	parts := splitFlowItems(inner)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		idx := strings.IndexByte(p, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(p[:idx])
		val := strings.TrimSpace(p[idx+1:])
		result[unquoteScalar(key)] = parseScalar(val)
	}
	return result
}

func splitFlowItems(s string) []string {
	var items []string
	depth := 0
	start := 0
	inSingle := false
	inDouble := false
	for i, ch := range s {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '[', '{':
			if !inSingle && !inDouble {
				depth++
			}
		case ']', '}':
			if !inSingle && !inDouble {
				depth--
			}
		case ',':
			if !inSingle && !inDouble && depth == 0 {
				items = append(items, s[start:i])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		items = append(items, s[start:])
	}
	return items
}

func parseScalar(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" || s == "~" || s == "null" || s == "Null" || s == "NULL" {
		return nil
	}

	unq := unquoteScalar(s)
	if unq != s {
		return unq
	}

	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	}

	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return int(i)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}

	return s
}

func unquoteScalar(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			inner := s[1 : len(s)-1]
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			inner = strings.ReplaceAll(inner, `\n`, "\n")
			inner = strings.ReplaceAll(inner, `\t`, "\t")
			return inner
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			inner := s[1 : len(s)-1]
			inner = strings.ReplaceAll(inner, `''`, `'`)
			return inner
		}
	}
	return s
}

// --- Marshaler ---

func marshalValue(buf *bytes.Buffer, v interface{}, indent int) error {
	if v == nil {
		buf.WriteString("{}\n")
		return nil
	}

	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			buf.WriteString("null\n")
			return nil
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Map:
		return marshalMap(buf, rv, indent)
	case reflect.Slice, reflect.Array:
		return marshalSlice(buf, rv, indent)
	case reflect.Struct:
		return marshalStruct(buf, rv, indent)
	default:
		writeScalar(buf, rv)
		buf.WriteByte('\n')
		return nil
	}
}

func marshalMap(buf *bytes.Buffer, rv reflect.Value, indent int) error {
	if rv.Len() == 0 {
		buf.WriteString("{}\n")
		return nil
	}

	keys := make([]string, 0, rv.Len())
	for _, k := range rv.MapKeys() {
		keys = append(keys, fmt.Sprint(k.Interface()))
	}
	sort.Strings(keys)

	prefix := strings.Repeat("  ", indent)
	for _, key := range keys {
		val := rv.MapIndex(reflect.ValueOf(key))
		buf.WriteString(prefix)
		buf.WriteString(yamlKey(key))
		buf.WriteString(":")

		if err := marshalInlineOrBlock(buf, val.Interface(), indent); err != nil {
			return err
		}
	}
	return nil
}

func marshalSlice(buf *bytes.Buffer, rv reflect.Value, indent int) error {
	if rv.Len() == 0 {
		buf.WriteString("[]\n")
		return nil
	}

	prefix := strings.Repeat("  ", indent)
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i)
		buf.WriteString(prefix)
		buf.WriteString("- ")

		val := elem.Interface()
		if isScalarValue(val) {
			writeScalar(buf, reflect.ValueOf(val))
			buf.WriteByte('\n')
		} else {
			buf.WriteByte('\n')
			if err := marshalValue(buf, val, indent+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func marshalStruct(buf *bytes.Buffer, rv reflect.Value, indent int) error {
	rt := rv.Type()
	prefix := strings.Repeat("  ", indent)

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("yaml")
		name, opts := parseTag(tag)
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}

		fv := rv.Field(i)
		if strings.Contains(opts, "omitempty") && fv.IsZero() {
			continue
		}

		buf.WriteString(prefix)
		buf.WriteString(yamlKey(name))
		buf.WriteString(":")

		if err := marshalInlineOrBlock(buf, fv.Interface(), indent); err != nil {
			return err
		}
	}
	return nil
}

func marshalInlineOrBlock(buf *bytes.Buffer, val interface{}, indent int) error {
	if val == nil {
		buf.WriteString(" null\n")
		return nil
	}
	if isScalarValue(val) {
		buf.WriteByte(' ')
		writeScalar(buf, reflect.ValueOf(val))
		buf.WriteByte('\n')
		return nil
	}

	rv := reflect.ValueOf(val)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			buf.WriteString(" null\n")
			return nil
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Map:
		if rv.Len() == 0 {
			buf.WriteString(" {}\n")
			return nil
		}
		buf.WriteByte('\n')
		return marshalMap(buf, rv, indent+1)
	case reflect.Slice, reflect.Array:
		if rv.Len() == 0 {
			buf.WriteString(" []\n")
			return nil
		}
		buf.WriteByte('\n')
		return marshalSlice(buf, rv, indent+1)
	case reflect.Struct:
		buf.WriteByte('\n')
		return marshalStruct(buf, rv, indent+1)
	default:
		buf.WriteByte(' ')
		writeScalar(buf, rv)
		buf.WriteByte('\n')
		return nil
	}
}

func isScalarValue(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return true
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		return false
	default:
		return true
	}
}

func writeScalar(buf *bytes.Buffer, rv reflect.Value) {
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			buf.WriteString("null")
			return
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.String:
		s := rv.String()
		if needsQuoting(s) {
			buf.WriteString(quoteString(s))
		} else {
			buf.WriteString(s)
		}
	case reflect.Bool:
		if rv.Bool() {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buf.WriteString(strconv.FormatInt(rv.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		buf.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		buf.WriteString(strconv.FormatFloat(rv.Float(), 'f', -1, 64))
	default:
		buf.WriteString(fmt.Sprintf("%v", rv.Interface()))
	}
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	if s == "~" || s == "null" || s == "Null" || s == "NULL" {
		return true
	}
	lower := strings.ToLower(s)
	if lower == "true" || lower == "false" || lower == "yes" || lower == "no" || lower == "on" || lower == "off" {
		return true
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	for _, ch := range s {
		if ch == ':' || ch == '#' || ch == '[' || ch == ']' || ch == '{' || ch == '}' ||
			ch == ',' || ch == '&' || ch == '*' || ch == '!' || ch == '|' || ch == '>' ||
			ch == '\'' || ch == '"' || ch == '%' || ch == '@' || ch == '`' || ch == '\n' {
			return true
		}
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' || s[0] == '-' || s[0] == '?' {
		return true
	}
	return false
}

func quoteString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}

func yamlKey(k string) string {
	if needsQuoting(k) {
		return quoteString(k)
	}
	return k
}

func parseTag(tag string) (string, string) {
	if tag == "" {
		return "", ""
	}
	parts := strings.SplitN(tag, ",", 2)
	name := parts[0]
	opts := ""
	if len(parts) > 1 {
		opts = parts[1]
	}
	return name, opts
}

// --- Struct unmarshaling ---

func unmarshalStruct(m map[string]interface{}, rv reflect.Value) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("yaml")
		name, _ := parseTag(tag)
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}

		val, ok := m[name]
		if !ok {
			continue
		}

		fv := rv.Field(i)
		if err := setField(fv, val); err != nil {
			return fmt.Errorf("yaml: field %s: %w", field.Name, err)
		}
	}
	return nil
}

func setField(fv reflect.Value, val interface{}) error {
	if val == nil {
		return nil
	}

	rv := reflect.ValueOf(val)
	ft := fv.Type()

	switch ft.Kind() {
	case reflect.String:
		fv.SetString(fmt.Sprint(val))
	case reflect.Bool:
		switch v := val.(type) {
		case bool:
			fv.SetBool(v)
		case string:
			b, _ := strconv.ParseBool(v)
			fv.SetBool(b)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch v := val.(type) {
		case int:
			fv.SetInt(int64(v))
		case float64:
			fv.SetInt(int64(v))
		case string:
			i, _ := strconv.ParseInt(v, 10, 64)
			fv.SetInt(i)
		}
	case reflect.Float32, reflect.Float64:
		switch v := val.(type) {
		case float64:
			fv.SetFloat(v)
		case int:
			fv.SetFloat(float64(v))
		case string:
			f, _ := strconv.ParseFloat(v, 64)
			fv.SetFloat(f)
		}
	case reflect.Slice:
		if rv.Kind() == reflect.Slice {
			elemType := ft.Elem()
			slice := reflect.MakeSlice(ft, 0, rv.Len())
			for j := 0; j < rv.Len(); j++ {
				elem := reflect.New(elemType).Elem()
				if err := setField(elem, rv.Index(j).Interface()); err != nil {
					return err
				}
				slice = reflect.Append(slice, elem)
			}
			fv.Set(slice)
		}
	case reflect.Map:
		if m, ok := val.(map[string]interface{}); ok {
			newMap := reflect.MakeMap(ft)
			for k, v := range m {
				keyVal := reflect.ValueOf(k)
				valElem := reflect.New(ft.Elem()).Elem()
				if err := setField(valElem, v); err != nil {
					return err
				}
				newMap.SetMapIndex(keyVal, valElem)
			}
			fv.Set(newMap)
		}
	case reflect.Struct:
		if m, ok := val.(map[string]interface{}); ok {
			return unmarshalStruct(m, fv)
		}
	case reflect.Interface:
		if val != nil {
			fv.Set(reflect.ValueOf(val))
		}
	default:
		if rv.Type().AssignableTo(ft) {
			fv.Set(rv)
		}
	}
	return nil
}

// --- Node API (subset for repos.go compatibility) ---

type NodeKind int

const (
	DocumentNode NodeKind = iota + 1
	MappingNode
	SequenceNode
	ScalarNode
)

type Style int

const (
	DoubleQuotedStyle Style = 1
)

type Node struct {
	Kind    NodeKind
	Value   string
	Style   Style
	Content []*Node
}

func valueToNode(val interface{}) *Node {
	if val == nil {
		return &Node{Kind: ScalarNode, Value: "null"}
	}
	switch v := val.(type) {
	case map[string]interface{}:
		node := &Node{Kind: MappingNode}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			node.Content = append(node.Content,
				&Node{Kind: ScalarNode, Value: k},
				valueToNode(v[k]),
			)
		}
		return node
	case []interface{}:
		node := &Node{Kind: SequenceNode}
		for _, item := range v {
			node.Content = append(node.Content, valueToNode(item))
		}
		return node
	case string:
		return &Node{Kind: ScalarNode, Value: v}
	case bool:
		return &Node{Kind: ScalarNode, Value: strconv.FormatBool(v)}
	case int:
		return &Node{Kind: ScalarNode, Value: strconv.Itoa(v)}
	case float64:
		return &Node{Kind: ScalarNode, Value: strconv.FormatFloat(v, 'f', -1, 64)}
	default:
		return &Node{Kind: ScalarNode, Value: fmt.Sprint(v)}
	}
}

// Encoder writes YAML to an io.Writer.
type Encoder struct {
	buf    *bytes.Buffer
	indent int
}

// NewEncoder returns an Encoder that writes to buf.
func NewEncoder(buf *bytes.Buffer) *Encoder {
	return &Encoder{buf: buf, indent: 2}
}

// SetIndent sets the indentation level (spaces per level).
func (e *Encoder) SetIndent(n int) {
	e.indent = n
}

// Encode writes a Node tree as YAML.
func (e *Encoder) Encode(n *Node) error {
	return e.encodeNode(n, 0, true)
}

// Close is a no-op for compatibility.
func (e *Encoder) Close() error { return nil }

func (e *Encoder) encodeNode(n *Node, depth int, topLevel bool) error {
	prefix := strings.Repeat(" ", depth*e.indent)

	switch n.Kind {
	case DocumentNode:
		for _, c := range n.Content {
			if err := e.encodeNode(c, 0, true); err != nil {
				return err
			}
		}
	case MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			e.buf.WriteString(prefix)
			e.buf.WriteString(key.Value)
			e.buf.WriteString(":")
			if val.Kind == ScalarNode {
				e.buf.WriteString(" ")
				e.writeScalarNode(val)
				e.buf.WriteString("\n")
			} else {
				e.buf.WriteString("\n")
				if err := e.encodeNode(val, depth+1, false); err != nil {
					return err
				}
			}
		}
	case SequenceNode:
		for _, item := range n.Content {
			e.buf.WriteString(prefix)
			e.buf.WriteString("- ")
			if item.Kind == ScalarNode {
				e.writeScalarNode(item)
				e.buf.WriteString("\n")
			} else {
				e.buf.WriteString("\n")
				if err := e.encodeNode(item, depth+1, false); err != nil {
					return err
				}
			}
		}
	case ScalarNode:
		if topLevel {
			e.writeScalarNode(n)
			e.buf.WriteString("\n")
		} else {
			e.buf.WriteString(prefix)
			e.writeScalarNode(n)
			e.buf.WriteString("\n")
		}
	}
	return nil
}

func (e *Encoder) writeScalarNode(n *Node) {
	if n.Style == DoubleQuotedStyle {
		e.buf.WriteString(`"`)
		e.buf.WriteString(strings.ReplaceAll(n.Value, `"`, `\"`))
		e.buf.WriteString(`"`)
	} else if needsQuoting(n.Value) {
		e.buf.WriteString(quoteString(n.Value))
	} else {
		e.buf.WriteString(n.Value)
	}
}
