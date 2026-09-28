package bdpwire

import (
	"encoding/json"
)

// Properties is a Resource's `properties` document: the one member of a
// record the protocol leaves open, so effective Type contracts can govern it.
// Members are carried as raw JSON, byte for byte, because a wire type has no
// business reinterpreting a domain document. A nil Properties marshals as {}:
// the member is required on every record and an authority never serves null
// for it.
type Properties map[string]json.RawMessage

// MarshalJSON writes {} for nil and the members otherwise.
func (p Properties) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]json.RawMessage(p))
}

// The bundle requires several array members outright — a descriptor's
// conformsTo, every collection's items — and an authority that serves null
// where the schema says array is nonconformant. encoding/json writes a nil
// slice as null, so each such member is a named slice type whose nil value
// marshals as []. Decoding is the default: [] on the wire yields an empty,
// non-nil slice, and round-trips.

// TypeIDs is the `typeIdArray` definition: unique absolute Type URLs, in no
// significant order, possibly empty where the spec permits an unconstrained
// endpoint or a root Type.
type TypeIDs []string

// MarshalJSON writes [] for nil.
func (s TypeIDs) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(s))
}

// BeadRecords is a page's worth of Bead records.
type BeadRecords []BeadRecord

// MarshalJSON writes [] for nil.
func (s BeadRecords) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]BeadRecord(s))
}

// LinkRecords is a page's worth of Link records, or one owned-Link entry.
type LinkRecords []LinkRecord

// MarshalJSON writes [] for nil.
func (s LinkRecords) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]LinkRecord(s))
}

// TypeSummaries is a page's worth of Type summaries.
type TypeSummaries []TypeSummary

// MarshalJSON writes [] for nil.
func (s TypeSummaries) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]TypeSummary(s))
}

// OwnedLinks is a Bead record's `ownedLinks` member: owned Link Type URL to
// the complete records of the owned Links of that Type, in ascending
// code-unit order of their canonical ids. An entry is present, possibly
// empty, for every Link Type the Bead's Type owns.
type OwnedLinks map[string]LinkRecords
