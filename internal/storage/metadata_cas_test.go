package storage

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

func raw(s string) *json.RawMessage {
	value := json.RawMessage(s)
	return &value
}

// TestCanonicalMetadataValueIgnoresFormatting pins the half of the equality
// rule that exists so a caller cannot lose a compare-and-set to its own
// encoder: whitespace and object key ORDER are not part of a value.
func TestCanonicalMetadataValueIgnoresFormatting(t *testing.T) {
	for _, test := range []struct {
		name  string
		left  string
		right string
	}{
		{"whitespace", `{ "a" : 1 }`, `{"a":1}`},
		{"top-level key order", `{"b":2,"a":1}`, `{"a":1,"b":2}`},
		{"nested key order", `{"o":{"z":1,"a":2}}`, `{"o":{"a":2,"z":1}}`},
		{"array whitespace", `[1, 2,  3]`, `[1,2,3]`},
		{"duplicate keys collapse to the last", `{"a":1,"a":2}`, `{"a":2}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal, err := MetadataValuesEqual(raw(test.left), raw(test.right))
			if err != nil {
				t.Fatalf("MetadataValuesEqual(%s, %s) error = %v", test.left, test.right, err)
			}
			if !equal {
				t.Fatalf("%s and %s compared unequal; the role promises canonical equality", test.left, test.right)
			}
		})
	}
}

// TestCanonicalMetadataValueKeepsNumbersLiteral pins the deliberate trade the
// rule makes. Decoding through float64 would make the first pair EQUAL — two
// distinct int64s past 2^53 — which is the silent false match a compare-and-set
// exists to prevent. The cost is the second pair, and it is the cost this test
// exists to keep visible.
func TestCanonicalMetadataValueKeepsNumbersLiteral(t *testing.T) {
	for _, test := range []struct {
		name  string
		left  string
		right string
	}{
		{"two int64s that float64 cannot tell apart", `9007199254740993`, `9007199254740992`},
		{"one integer written two ways", `1`, `1.0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal, err := MetadataValuesEqual(raw(test.left), raw(test.right))
			if err != nil {
				t.Fatalf("MetadataValuesEqual error = %v", err)
			}
			if equal {
				t.Fatalf("%s and %s compared EQUAL; numbers are compared as their source literal", test.left, test.right)
			}
		})
	}
}

// TestMetadataValuesEqualTreatsNilAsAbsent pins that an absent key equals only
// an absent key, and in particular that a stored JSON null is PRESENT.
func TestMetadataValuesEqualTreatsNilAsAbsent(t *testing.T) {
	for _, test := range []struct {
		name  string
		left  *json.RawMessage
		right *json.RawMessage
		want  bool
	}{
		{"absent equals absent", nil, nil, true},
		{"absent does not equal null", nil, raw(`null`), false},
		{"null does not equal absent", raw(`null`), nil, false},
		{"null equals null", raw(`null`), raw(`null`), true},
		{"absent does not equal the empty string", nil, raw(`""`), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal, err := MetadataValuesEqual(test.left, test.right)
			if err != nil {
				t.Fatalf("MetadataValuesEqual error = %v", err)
			}
			if equal != test.want {
				t.Fatalf("MetadataValuesEqual = %v, want %v", equal, test.want)
			}
		})
	}
}

// TestCanonicalMetadataValueRefusesMalformedInput pins that a value the role
// would otherwise store is refused before it reaches a substrate. Trailing
// content matters as much as a broken token: `1 2` parses one value and would
// silently drop the rest.
func TestCanonicalMetadataValueRefusesMalformedInput(t *testing.T) {
	for _, value := range []string{``, `{`, `{"a":}`, `1 2`, `"unterminated`} {
		t.Run(value, func(t *testing.T) {
			if _, err := CanonicalMetadataValue(json.RawMessage(value)); err == nil {
				t.Fatalf("CanonicalMetadataValue(%q) error = nil, want a refusal", value)
			}
		})
	}
}

// TestPlanCompareAndSetKeyRefusesUnusableRequests pins every refusal the role
// documents as ErrValidation, at the one function all three legs call before
// they open a transaction.
func TestPlanCompareAndSetKeyRefusesUnusableRequests(t *testing.T) {
	for _, test := range []struct {
		name    string
		request issueops.CompareAndSetKeyRequest
	}{
		{"empty actor", issueops.CompareAndSetKeyRequest{IssueID: "bd-1", Key: "lease"}},
		{"empty issue id", issueops.CompareAndSetKeyRequest{Actor: "tester", Key: "lease"}},
		{"empty key", issueops.CompareAndSetKeyRequest{Actor: "tester", IssueID: "bd-1"}},
		{"key outside the syntax", issueops.CompareAndSetKeyRequest{Actor: "tester", IssueID: "bd-1", Key: "1bad"}},
		{"malformed expected", issueops.CompareAndSetKeyRequest{
			Actor: "tester", IssueID: "bd-1", Key: "lease", Expected: raw(`{`)}},
		{"malformed value", issueops.CompareAndSetKeyRequest{
			Actor: "tester", IssueID: "bd-1", Key: "lease", Value: raw(`nope`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PlanCompareAndSetKey(test.request); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("PlanCompareAndSetKey(%s) error = %v, want ErrValidation", test.name, err)
			}
		})
	}
}

// TestPlanCompareAndSetKeyCanonicalizesWithoutTouchingTheRequest pins both
// halves of the plan's promise: the values it carries are canonical, and the
// caller's request is not written through.
func TestPlanCompareAndSetKeyCanonicalizesWithoutTouchingTheRequest(t *testing.T) {
	expected := json.RawMessage(`{ "b":2, "a":1 }`)
	value := json.RawMessage(`{ "z" : true }`)
	request := issueops.CompareAndSetKeyRequest{
		Actor: "tester", IssueID: "bd-1", Key: "gc.lease",
		Expected: &expected, Value: &value,
	}

	plan, err := PlanCompareAndSetKey(request)
	if err != nil {
		t.Fatalf("PlanCompareAndSetKey: %v", err)
	}
	if got := string(*plan.Expected); got != `{"a":1,"b":2}` {
		t.Errorf("plan.Expected = %s, want the canonical encoding", got)
	}
	if got := string(*plan.Value); got != `{"z":true}` {
		t.Errorf("plan.Value = %s, want the canonical encoding", got)
	}
	if string(expected) != `{ "b":2, "a":1 }` || string(value) != `{ "z" : true }` {
		t.Errorf("the request's values were rewritten: expected = %s, value = %s", expected, value)
	}
	if plan.Expected == &expected || plan.Value == &value {
		t.Error("the plan aliases the caller's pointers; it must carry copies")
	}
}

// TestPlanCompareAndSetKeyKeepsAbsence pins that nil survives planning on both
// sides — the two transitions that make a create and a delete expressible.
func TestPlanCompareAndSetKeyKeepsAbsence(t *testing.T) {
	plan, err := PlanCompareAndSetKey(issueops.CompareAndSetKeyRequest{
		Actor: "tester", IssueID: "bd-1", Key: "lease",
	})
	if err != nil {
		t.Fatalf("PlanCompareAndSetKey: %v", err)
	}
	if plan.Expected != nil || plan.Value != nil {
		t.Fatalf("plan = %+v, want both values nil: nil means ABSENT and is not a default to fill in", plan)
	}
}
