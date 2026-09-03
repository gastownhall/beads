package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/issueops"
)

// The DATABASE-FREE half of issueops.MetadataCAS: what a compare-and-set
// request means and when two metadata values are the same value. Every
// implementation of the role runs these, so the equality rule the role promises
// has ONE definition rather than one per backend — and it is pinned by unit
// tests in milliseconds, where a contract case would need a database to observe
// the same thing.
//
// IT IS HERE RATHER THAN IN internal/workapi, where the sibling roles put their
// request validation, and the reason is a dependency direction rather than
// taste. The unit-of-work leg reaches the shared body through the domain issue
// repository, so internal/storage/domain has to name the plan type — and
// workapi already imports domain. Beside ValidateMetadataKey and
// MergeMetadataJSON is where the rest of this plane's meaning lives anyway, and
// the hook decorator asks the same equality question from this very package.
//
// What is NOT here is the swap itself. Reading a key, comparing it and writing
// the object back need one transaction (issueops.MetadataCAS.CompareAndSetKey),
// which no interface above a store publishes; the body lives in
// internal/storage/issueops/metadata_cas.go and all three legs reach it.

// CompareAndSetKeyPlan is a validated compare-and-set request with both of its
// JSON values in canonical form. It is what an implementation works from, so
// the request the caller owns is never written through and the comparison the
// role promises is made on canonical bytes on both sides.
type CompareAndSetKeyPlan struct {
	// Actor, IssueID and Key are the request's, checked non-empty and
	// well-formed.
	Actor   string
	IssueID string
	Key     string
	// Expected is the canonical encoding of the value the key must hold, or nil
	// when the request requires the key to be ABSENT.
	Expected *json.RawMessage
	// Value is the canonical encoding of the value to store, or nil when the
	// request removes the key.
	Value *json.RawMessage
}

// PlanCompareAndSetKey validates a compare-and-set request and canonicalizes
// its values. It is the whole of the role's request validation: every
// implementation calls it before touching a substrate, so a refused request
// costs no database work anywhere.
//
// It COPIES both raw values rather than aliasing the caller's, because the
// canonical form is written into the plan and the request belongs to the caller
// for the whole call.
func PlanCompareAndSetKey(in issueops.CompareAndSetKeyRequest) (CompareAndSetKeyPlan, error) {
	if in.Actor == "" {
		return CompareAndSetKeyPlan{}, fmt.Errorf(
			"%w: compare-and-set requires an actor to attribute the swap to", issueops.ErrValidation)
	}
	if in.IssueID == "" {
		return CompareAndSetKeyPlan{}, fmt.Errorf(
			"%w: compare-and-set requires an issue id", issueops.ErrValidation)
	}
	if err := ValidateMetadataKey(in.Key); err != nil {
		return CompareAndSetKeyPlan{}, fmt.Errorf("%w: %v", issueops.ErrValidation, err)
	}
	plan := CompareAndSetKeyPlan{Actor: in.Actor, IssueID: in.IssueID, Key: in.Key}
	var err error
	if plan.Expected, err = CanonicalMetadataPointer(in.Expected); err != nil {
		return CompareAndSetKeyPlan{}, fmt.Errorf("%w: expected value for metadata key %q: %v",
			issueops.ErrValidation, in.Key, err)
	}
	if plan.Value, err = CanonicalMetadataPointer(in.Value); err != nil {
		return CompareAndSetKeyPlan{}, fmt.Errorf("%w: new value for metadata key %q: %v",
			issueops.ErrValidation, in.Key, err)
	}
	return plan, nil
}

// CanonicalMetadataValue returns raw's canonical encoding: the encoding two
// JSON metadata values share exactly when issueops.MetadataCAS calls them
// equal.
//
// Insignificant whitespace goes and object keys are emitted in sorted order, so
// a value re-serialized by a different encoder still matches — the property
// that keeps a caller from losing a compare-and-set to its own formatting.
// Duplicate keys in one object collapse to the last, which is what every JSON
// reader in this tree already does with them.
//
// NUMBERS KEEP THEIR SOURCE LITERAL, so 1 and 1.0 canonicalize differently and
// do not match. This function does not round-trip a number through float64, and
// the reason is NOT that doing so would lose precision the store keeps: the
// metadata column loses it first. go-mysql-server decodes JSON numbers into
// float64 and re-emits them, measured — 9007199254740993 is stored as
// ...992, 1.0 as 1, -0.0 as 0, 1e300 as three hundred and one digits. So the
// substrate's own fidelity, not this rule, is what bounds a numeric value.
//
// What the literal rule buys is that this function stays a pure statement about
// JSON rather than a copy of one engine's number handling — a copy that would
// silently equate two values a TEXT-column backend can hold apart, on the one
// comparison a compare-and-set exists to make. What it COSTS is that a caller
// composing an expectation from its own spelling of a number can disagree with
// the row; the role answers that by making Current the value the ROW holds, so
// the documented loop converges. See issueops.CompareAndSetKeyRequest.Expected.
func CanonicalMetadataValue(raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("not a well-formed JSON value: %q", truncateMetadataValue(raw))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("not a well-formed JSON value: %w", err)
	}
	var buf bytes.Buffer
	if err := writeCanonicalMetadataJSON(&buf, value); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}

// MetadataValuesEqual reports whether two optional metadata values are the same
// value under the canonical rule, with nil meaning ABSENT on either side. An
// absent key equals only an absent key: a key stored holding JSON null is
// present, and the metadata object can show the difference.
//
// Both sides are canonicalized here rather than assumed canonical, so it
// answers for raw caller input as well as for stored bytes.
func MetadataValuesEqual(a, b *json.RawMessage) (bool, error) {
	if a == nil || b == nil {
		return a == nil && b == nil, nil
	}
	left, err := CanonicalMetadataValue(*a)
	if err != nil {
		return false, err
	}
	right, err := CanonicalMetadataValue(*b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}

// CanonicalMetadataPointer canonicalizes an optional value, preserving the nil
// that means "absent".
func CanonicalMetadataPointer(raw *json.RawMessage) (*json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	canonical, err := CanonicalMetadataValue(*raw)
	if err != nil {
		return nil, err
	}
	return &canonical, nil
}

// writeCanonicalMetadataJSON emits one decoded JSON value in canonical form.
// The value comes from a decoder with UseNumber set, so every number arrives as
// its source literal and is written back unchanged.
func writeCanonicalMetadataJSON(buf *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if typed {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(typed.String())
	case string:
		return writeCanonicalMetadataString(buf, typed)
	case []any:
		buf.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalMetadataJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalMetadataString(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonicalMetadataJSON(buf, typed[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		// encoding/json produces only the cases above for an `any` target with
		// UseNumber set, so reaching this means the decoder changed under us.
		return fmt.Errorf("cannot canonicalize %T", value)
	}
	return nil
}

// writeCanonicalMetadataString emits a JSON string with the standard library's
// escaping, which both sides of a comparison go through identically.
func writeCanonicalMetadataString(buf *bytes.Buffer, s string) error {
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding string: %w", err)
	}
	buf.Write(encoded)
	return nil
}

// truncateMetadataValue bounds a malformed value quoted back at a caller, so a
// megabyte of junk does not become a megabyte of error message.
func truncateMetadataValue(raw json.RawMessage) string {
	const limit = 64
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "…"
}
