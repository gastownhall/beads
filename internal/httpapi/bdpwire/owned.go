package bdpwire

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
)

// OwnedWildcardDeclaration is the closed max-only wildcard envelope. It has
// no label because it names no single Link Type.
type OwnedWildcardDeclaration struct {
	Max int `json:"max"`
}

// OwnedOutgoingDeclarations separates the reserved wildcard from explicit
// Link Type declarations. Types must never contain the reserved "*" key.
// A nil TypeDescriptor.OwnsOutgoing omits the optional declaration altogether.
type OwnedOutgoingDeclarations struct {
	Wildcard *OwnedWildcardDeclaration       `json:"*,omitempty"`
	Types    map[string]OwnedLinkDeclaration `json:"-"`
}

// JSON Schema uses ECMAScript regex semantics: its dot excludes all four
// line terminators. Go's dot excludes only LF. Preserve the source pattern's
// prefix match; this is not a stricter URL parser or an end-anchored grammar.
var ownedTypeURLPattern = regexp.MustCompile(`^https?://[^\r\n\x{2028}\x{2029}]+`)

// Validate checks declaration bounds, including the cross-entry whole-set
// bound that JSON Schema cannot express. Record grouping remains graph law.
// A nil receiver represents an absent optional declaration and is valid.
func (d *OwnedOutgoingDeclarations) Validate() error {
	if d == nil {
		return nil
	}
	return d.validate("ownsOutgoing")
}

func (d OwnedOutgoingDeclarations) validate(path string) error {
	if d.Wildcard == nil && len(d.Types) == 0 {
		return fmt.Errorf("bdpwire: %s: must not be empty", path)
	}
	if d.Wildcard != nil && d.Wildcard.Max < 1 {
		return fmt.Errorf("bdpwire: %s.*.max: must be positive", path)
	}
	for key, entry := range d.Types {
		// This is the pinned absoluteHttpUrl grammar, not URL normalization.
		if key == "*" {
			return fmt.Errorf("bdpwire: %s: reserved key %q belongs in Wildcard, not Types", path, key)
		}
		if err := validateOwnedTypeKey(key, path); err != nil {
			return err
		}
		if entry.Max < 1 {
			return fmt.Errorf("bdpwire: %s.%s.max: must be positive", path, key)
		}
		if d.Wildcard != nil && entry.Max > d.Wildcard.Max {
			return fmt.Errorf("bdpwire: %s.%s.max: exceeds wildcard max", path, key)
		}
	}
	return nil
}

func (d OwnedOutgoingDeclarations) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	members := make(map[string]any, len(d.Types)+1)
	for key, entry := range d.Types {
		members[key] = entry
	}
	if d.Wildcard != nil {
		members["*"] = d.Wildcard
	}
	return json.Marshal(members)
}

func (d *OwnedOutgoingDeclarations) UnmarshalJSON(data []byte) error {
	raw, err := oneDocument(data)
	if err != nil {
		return err
	}
	return decodeOwnedOutgoing(raw, d, "ownsOutgoing")
}

func decodeOwnedOutgoing(raw json.RawMessage, target *OwnedOutgoingDeclarations, path string) error {
	if jsonTypeOf(raw) != "object" {
		return typeError(path, "object", raw)
	}
	members, err := splitObject(raw)
	if err != nil {
		return fmt.Errorf("bdpwire: %s: %w", path, err)
	}
	result := OwnedOutgoingDeclarations{Types: map[string]OwnedLinkDeclaration{}}
	for _, m := range members {
		if m.name == "*" {
			var wildcard OwnedWildcardDeclaration
			if err := decodeValue(m.value, reflect.ValueOf(&wildcard).Elem(), path+".*"); err != nil {
				return err
			}
			result.Wildcard = &wildcard
		} else {
			var entry OwnedLinkDeclaration
			if err := decodeValue(m.value, reflect.ValueOf(&entry).Elem(), path+"."+m.name); err != nil {
				return err
			}
			// Optional absence is valid; an explicitly present empty label is not.
			fields, err := splitObject(m.value)
			if err != nil {
				return fmt.Errorf("bdpwire: %s.%s: %w", path, m.name, err)
			}
			for _, field := range fields {
				if field.name == "label" && entry.Label == "" {
					return fmt.Errorf("bdpwire: %s.%s.label: must not be empty", path, m.name)
				}
			}
			result.Types[m.name] = entry
		}
	}
	if err := result.validate(path); err != nil {
		return err
	}
	*target = result
	return nil
}

// validateOwnedTypeKey holds both declaration and record keys to the pinned
// absoluteHttpUrl pattern. Endpoint, grouping and Type membership laws belong
// to the domain layer; this check does not perform URL normalization.
func validateOwnedTypeKey(key, path string) error {
	if !ownedTypeURLPattern.MatchString(key) {
		return fmt.Errorf("bdpwire: %s: owned Link Type key must match absoluteHttpUrl: %q", path, key)
	}
	return nil
}

// Validate checks only the ownedLinks key grammar. Empty maps and explicit
// empty groups are valid; record membership and ordering remain graph law.
func (o OwnedLinks) Validate() error {
	for key := range o {
		if err := validateOwnedTypeKey(key, "ownedLinks"); err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON validates keys without changing nil-map or empty-group encoding.
func (o OwnedLinks) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]LinkRecords(o))
}

// UnmarshalJSON checks keys while retaining encoding/json's ordinary record
// decoding. Unmarshal additionally applies the package's strict shape checks.
func (o *OwnedLinks) UnmarshalJSON(data []byte) error {
	var result map[string]LinkRecords
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("bdpwire: ownedLinks: %w", err)
	}
	value := OwnedLinks(result)
	if err := value.Validate(); err != nil {
		return err
	}
	*o = value
	return nil
}
