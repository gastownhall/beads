package bdpwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// STRICT DECODING (P0 council, 2026-09-07). encoding/json's DisallowUnknownFields
// is not a closed shape: it matches member names case-insensitively, so
// {"uri":"urn:x","revision":"r","URI":"urn:y"} passed the closed pinnedReference
// envelope and changed the URI; it maps an explicit null onto the zero value,
// so archivedAt:null became absence and a validator could not see the
// member; it cannot tell an absent required member from a present one; and
// it decodes an integer member through Go's int parser, which refuses the
// schema-valid spellings 1.0 and 1e0. The decoder below reads the document
// itself, member by member, and holds it to the bundle's structural facts:
//
//   - member names are EXACT and case-sensitive; a stranger, a case variant
//     and a duplicate are refused ("unknown field" / "duplicate member");
//   - a REQUIRED member (a field without omitempty — the parity test welds
//     that to the bundle's required lists) must be present;
//   - NULL is refused wherever the bundle gives no null: every member but a
//     collection's `next`, the one required pointer. Absent and null are
//     different things, and the bundle admits only absence;
//   - a member must have its JSON type — a string is not a number, an array
//     is not null, an object is not an array;
//   - an INTEGER member decodes any RFC 8259 spelling whose mathematical
//     value is an integer (1, 1.0, 1e0, 100e-2, 1E+2), exactly, never through
//     a float, within the range of Go's int — 64 bits on every platform this
//     tree ships for, so -9223372036854775808 through 9223372036854775807;
//     a fractional value and a value outside that range are refused.
//     DECISION: that range is the documented limit for every integer member
//     (the bundle's positiveInteger and readProblem.status have no maximum of
//     their own); the schema-level minimum (1) is a model law, not a shape.
//
// Reference (the string-or-object sum) and ReadProblem (the one open
// envelope) are built on the same primitives, so a Reference nested in a
// LinkRecord and a ReadProblem's named members are held to the same rules.
// A caller that wants a lenient read uses encoding/json directly (doc.go).

// Unmarshal decodes one JSON document into v strictly, under the rules
// above; anything after the document is an error. v must be a non-nil
// pointer to one of this package's types (or a slice, map, string or int of
// them).
func Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("bdpwire: Unmarshal target must be a non-nil pointer")
	}
	raw, err := oneDocument(data)
	if err != nil {
		return err
	}
	return decodeValue(raw, rv.Elem(), rv.Elem().Type().Name())
}

// Decode is Unmarshal over a reader.
func Decode(r io.Reader, v any) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return Unmarshal(data, v)
}

// oneDocument checks that data holds exactly one JSON value and returns it.
// The decoder validates the whole value's syntax as it scans it, so every
// raw slice cut from it afterwards is well-formed JSON.
func oneDocument(data []byte) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("bdpwire: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("bdpwire: trailing data after JSON document")
		}
		return nil, fmt.Errorf("bdpwire: %w", err)
	}
	return raw, nil
}

// member is one object member as it appeared, name and raw value.
type member struct {
	name  string
	value json.RawMessage
}

// splitObject returns an object's members in document order; a duplicate
// name is refused. The input is a well-formed object (oneDocument).
func splitObject(raw json.RawMessage) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // '{'
		return nil, err
	}
	var members []member
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name := tok.(string) // an object key is always a string token
		if seen[name] {
			return nil, fmt.Errorf("duplicate member %q", name)
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, member{name: name, value: value})
	}
	return members, nil
}

// splitArray returns an array's elements in order. The input is a
// well-formed array.
func splitArray(raw json.RawMessage) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // '['
		return nil, err
	}
	elems := []json.RawMessage{}
	for dec.More() {
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		elems = append(elems, value)
	}
	return elems, nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// jsonTypeOf names a raw value's JSON type for a diagnostic.
func jsonTypeOf(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "nothing"
	}
	switch raw[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 'n':
		return "null"
	case 't', 'f':
		return "boolean"
	}
	return "number"
}

func typeError(path, want string, raw json.RawMessage) error {
	return fmt.Errorf("bdpwire: %s: expected a JSON %s, got %s", path, want, jsonTypeOf(raw))
}

var (
	rawMessageType    = reflect.TypeOf(json.RawMessage{})
	referenceType     = reflect.TypeOf(Reference{})
	problemType       = reflect.TypeOf(ReadProblem{})
	ownedOutgoingType = reflect.TypeOf(OwnedOutgoingDeclarations{})
	ownedLinksType    = reflect.TypeOf(OwnedLinks{})
)

// decodeValue decodes raw into target, which is settable. path names the
// member for diagnostics.
func decodeValue(raw json.RawMessage, target reflect.Value, path string) error {
	t := target.Type()
	switch t {
	case rawMessageType:
		target.SetBytes(append([]byte(nil), raw...))
		return nil
	case referenceType:
		return decodeReference(raw, target.Addr().Interface().(*Reference), path)
	case ownedOutgoingType:
		return decodeOwnedOutgoing(raw, target.Addr().Interface().(*OwnedOutgoingDeclarations), path)
	case problemType:
		return decodeProblem(raw, target.Addr().Interface().(*ReadProblem), path)
	}
	switch t.Kind() {
	case reflect.Pointer:
		// Whether null is admissible is a fact about the MEMBER; decodeStruct
		// settles it before coming here, so a null that arrives is one the
		// bundle does not admit.
		if isNull(raw) {
			return fmt.Errorf("bdpwire: %s: must not be null", path)
		}
		elem := reflect.New(t.Elem())
		if err := decodeValue(raw, elem.Elem(), path); err != nil {
			return err
		}
		target.Set(elem)
		return nil
	case reflect.Struct:
		return decodeStruct(raw, target, path)
	case reflect.String:
		if jsonTypeOf(raw) != "string" {
			return typeError(path, "string", raw)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("bdpwire: %s: %w", path, err)
		}
		target.SetString(s)
		return nil
	case reflect.Int:
		n, err := parseJSONInteger(raw)
		if err != nil {
			return fmt.Errorf("bdpwire: %s: %w", path, err)
		}
		target.SetInt(n)
		return nil
	case reflect.Slice:
		if jsonTypeOf(raw) != "array" {
			return typeError(path, "array", raw)
		}
		elems, err := splitArray(raw)
		if err != nil {
			return fmt.Errorf("bdpwire: %s: %w", path, err)
		}
		slice := reflect.MakeSlice(t, len(elems), len(elems))
		for i, e := range elems {
			if err := decodeValue(e, slice.Index(i), path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
		target.Set(slice)
		return nil
	case reflect.Map:
		if jsonTypeOf(raw) != "object" {
			return typeError(path, "object", raw)
		}
		members, err := splitObject(raw)
		if err != nil {
			return fmt.Errorf("bdpwire: %s: %w", path, err)
		}
		m := reflect.MakeMapWithSize(t, len(members))
		for _, mem := range members {
			if t == ownedLinksType {
				if err := validateOwnedTypeKey(mem.name, path); err != nil {
					return err
				}
			}
			v := reflect.New(t.Elem()).Elem()
			if err := decodeValue(mem.value, v, path+"."+mem.name); err != nil {
				return err
			}
			m.SetMapIndex(reflect.ValueOf(mem.name).Convert(t.Key()), v)
		}
		target.Set(m)
		return nil
	}
	return fmt.Errorf("bdpwire: %s: cannot decode a JSON %s into %s", path, jsonTypeOf(raw), t)
}

// structField is one wire member of a struct: its field index, its exact
// name, whether the bundle requires it (no omitempty), and whether it is the
// one member that may be null (a required pointer: a collection's next).
type structField struct {
	index    int
	name     string
	required bool
	nullable bool
}

type structInfo struct {
	byName map[string]structField
	fields []structField
}

var structInfos sync.Map // reflect.Type → *structInfo

// structFields reads a struct's wire members off its json tags, once.
func structFields(t reflect.Type) *structInfo {
	if info, ok := structInfos.Load(t); ok {
		return info.(*structInfo)
	}
	info := &structInfo{byName: map[string]structField{}}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		omitempty := false
		for _, opt := range strings.Split(opts, ",") {
			if opt == "omitempty" {
				omitempty = true
			}
		}
		sf := structField{index: i, name: name, required: !omitempty, nullable: !omitempty && f.Type.Kind() == reflect.Pointer}
		info.byName[name] = sf
		info.fields = append(info.fields, sf)
	}
	structInfos.Store(t, info)
	return info
}

func decodeStruct(raw json.RawMessage, target reflect.Value, path string) error {
	if jsonTypeOf(raw) != "object" {
		return typeError(path, "object", raw)
	}
	members, err := splitObject(raw)
	if err != nil {
		return fmt.Errorf("bdpwire: %s: %w", path, err)
	}
	return decodeStructMembers(members, target, path)
}

// decodeStructMembers is the closed-envelope rule: every member names a
// field exactly, null is refused except on the nullable member, and every
// required member is present.
func decodeStructMembers(members []member, target reflect.Value, path string) error {
	info := structFields(target.Type())
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		f, ok := info.byName[m.name]
		if !ok {
			return fmt.Errorf("bdpwire: %s: unknown field %q (member names are exact and case-sensitive)", path, m.name)
		}
		seen[m.name] = true
		if isNull(m.value) {
			if f.nullable {
				continue // present and null: the pointer stays nil
			}
			return fmt.Errorf("bdpwire: %s.%s: must not be null (the bundle admits absence, not null)", path, m.name)
		}
		if err := decodeValue(m.value, target.Field(f.index), path+"."+m.name); err != nil {
			return err
		}
	}
	for _, f := range info.fields {
		if f.required && !seen[f.name] {
			return fmt.Errorf("bdpwire: %s: missing required member %q", path, f.name)
		}
	}
	return nil
}

// decodeReference reads the bundle's reference sum: a URI string, or a
// pinned-reference object held to the closed pinnedReference shape with a
// nonempty revision (see Reference).
func decodeReference(raw json.RawMessage, r *Reference, path string) error {
	raw = bytes.TrimSpace(raw)
	switch jsonTypeOf(raw) {
	case "string":
		var uri string
		if err := json.Unmarshal(raw, &uri); err != nil {
			return fmt.Errorf("bdpwire: %s: %w", path, err)
		}
		*r = Reference{URI: uri}
		return nil
	case "object":
		var pinned pinnedReferenceJSON
		if err := decodeStruct(raw, reflect.ValueOf(&pinned).Elem(), path); err != nil {
			return err
		}
		if pinned.Revision == "" {
			return fmt.Errorf("bdpwire: %s: pinned reference revision must be nonempty", path)
		}
		*r = Reference{URI: pinned.URI, Revision: pinned.Revision}
		return nil
	}
	return fmt.Errorf("bdpwire: %s: reference must be a URI string or a pinned-reference object, got %s", path, jsonTypeOf(raw))
}

// decodeProblem splits a problem into its protocol-owned members, decoded
// under the closed rule, and its RFC 9457 extension members, kept verbatim.
func decodeProblem(raw json.RawMessage, p *ReadProblem, path string) error {
	if jsonTypeOf(raw) != "object" {
		return fmt.Errorf("bdpwire: %s: problem must be an object, got %s", path, jsonTypeOf(raw))
	}
	members, err := splitObject(raw)
	if err != nil {
		return fmt.Errorf("bdpwire: %s: %w", path, err)
	}
	var named []member
	var extensions map[string]json.RawMessage
	for _, m := range members {
		if readProblemMembers[m.name] {
			named = append(named, m)
			continue
		}
		if extensions == nil {
			extensions = map[string]json.RawMessage{}
		}
		extensions[m.name] = append(json.RawMessage(nil), m.value...)
	}
	var decoded readProblemMembersOnly
	if err := decodeStructMembers(named, reflect.ValueOf(&decoded).Elem(), path); err != nil {
		return err
	}
	result := ReadProblem(decoded)
	result.Extensions = extensions
	if err := result.validateErasedPointer(); err != nil {
		return err
	}
	*p = result
	return nil
}

// parseJSONInteger decodes a JSON number whose mathematical value is an
// integer — in any RFC 8259 spelling: 1, 1.0, 1e0, 100e-2, 1E+2, -0 — into
// the platform int, exactly, without passing through a float. A fractional
// value (1.5, 1e-1), a value outside the int range, and a token that is not
// a number are errors. The range is Go's int: 64 bits on every platform this
// tree ships for (strconv.IntSize decides).
func parseJSONInteger(raw json.RawMessage) (int64, error) {
	s := string(bytes.TrimSpace(raw))
	if jsonTypeOf(raw) != "number" {
		return 0, fmt.Errorf("expected a JSON integer, got %s", jsonTypeOf(raw))
	}
	neg := false
	if s[0] == '-' {
		neg, s = true, s[1:]
	}
	mantissa, expText, _ := strings.Cut(strings.ToLower(s), "e")
	intPart, frac, _ := strings.Cut(mantissa, ".")
	exp := 0
	if expText != "" {
		sign := 1
		if expText[0] == '+' || expText[0] == '-' {
			if expText[0] == '-' {
				sign = -1
			}
			expText = expText[1:]
		}
		for i := 0; i < len(expText); i++ {
			exp = exp*10 + int(expText[i]-'0')
			if exp > 1000 {
				break // already past any integer int holds or any fraction that could vanish
			}
		}
		exp *= sign
	}
	// The exact value is 0.digits × 10^(exp10 + len(digits)) with the
	// leading and trailing zeros stripped off the digit string.
	digits := strings.TrimLeft(intPart+frac, "0")
	exp10 := exp - len(frac)
	trimmed := strings.TrimRight(digits, "0")
	exp10 += len(digits) - len(trimmed)
	digits = trimmed
	if digits == "" {
		return 0, nil // every zero, -0 and 0.0e5 included
	}
	if exp10 < 0 {
		return 0, fmt.Errorf("%s is not an integer", raw)
	}
	if len(digits)+exp10 > 19 {
		return 0, fmt.Errorf("%s is outside the int range", raw)
	}
	text := digits + strings.Repeat("0", exp10)
	if neg {
		text = "-" + text
	}
	n, err := strconv.ParseInt(text, 10, strconv.IntSize)
	if err != nil {
		return 0, fmt.Errorf("%s is outside the int range", raw)
	}
	return n, nil
}
