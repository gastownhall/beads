package types

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStorageClassIsValid(t *testing.T) {
	valid := []StorageClass{"", StorageClassVersioned, StorageClassUnversioned, StorageClassEphemeral}
	for _, s := range valid {
		if !s.IsValid() {
			t.Errorf("IsValid(%q) = false, want true", s)
		}
	}
	invalid := []StorageClass{"Versioned", "durable", "wisp", "none"}
	for _, s := range invalid {
		if s.IsValid() {
			t.Errorf("IsValid(%q) = true, want false", s)
		}
	}
}

func TestParseStorageClass(t *testing.T) {
	if _, err := ParseStorageClass(""); err == nil {
		t.Error("ParseStorageClass(\"\") should reject empty (callers gate unset before parsing)")
	}
	if _, err := ParseStorageClass("bogus"); err == nil {
		t.Error("ParseStorageClass(\"bogus\") should fail")
	} else if !strings.Contains(err.Error(), "versioned, unversioned, or ephemeral") {
		t.Errorf("error should enumerate valid values, got: %v", err)
	}
	got, err := ParseStorageClass("unversioned")
	if err != nil || got != StorageClassUnversioned {
		t.Errorf("ParseStorageClass(unversioned) = %q, %v", got, err)
	}
}

func TestStorageClassNormalize(t *testing.T) {
	if got := StorageClassVersioned.Normalize(); got != "" {
		t.Errorf("versioned should normalize to unset, got %q", got)
	}
	if got := StorageClassUnversioned.Normalize(); got != StorageClassUnversioned {
		t.Errorf("unversioned should survive normalize, got %q", got)
	}
	if got := StorageClass("").Normalize(); got != "" {
		t.Errorf("unset should stay unset, got %q", got)
	}
}

func TestEffectiveStorageClass(t *testing.T) {
	cases := []struct {
		name  string
		issue Issue
		want  StorageClass
	}{
		{"default is versioned", Issue{}, StorageClassVersioned},
		{"wisp plane is ephemeral", Issue{Ephemeral: true}, StorageClassEphemeral},
		{"no-history plane is ephemeral", Issue{NoHistory: true}, StorageClassEphemeral},
		{"explicit wins", Issue{StorageClass: StorageClassUnversioned}, StorageClassUnversioned},
		{"explicit ephemeral on wisp plane", Issue{Ephemeral: true, StorageClass: StorageClassEphemeral}, StorageClassEphemeral},
	}
	for _, tc := range cases {
		if got := tc.issue.EffectiveStorageClass(); got != tc.want {
			t.Errorf("%s: EffectiveStorageClass() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func validBaseIssue() Issue {
	return Issue{
		ID:        "bd-sc1",
		Title:     "t",
		Status:    StatusOpen,
		IssueType: TypeTask,
		Priority:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func TestIssueValidateStorageClass(t *testing.T) {
	// Unknown value rejected.
	bad := validBaseIssue()
	bad.StorageClass = "durable"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "invalid storage class") {
		t.Errorf("unknown class: got %v", err)
	}

	// Durable class on a wisp-plane record rejected.
	wispDurable := validBaseIssue()
	wispDurable.Ephemeral = true
	wispDurable.StorageClass = StorageClassUnversioned
	if err := wispDurable.Validate(); err == nil || !strings.Contains(err.Error(), "conflicts with ephemeral") {
		t.Errorf("durable-on-wisp: got %v", err)
	}

	// Ephemeral class without the wisp plane rejected.
	looseEphemeral := validBaseIssue()
	looseEphemeral.StorageClass = StorageClassEphemeral
	if err := looseEphemeral.Validate(); err == nil || !strings.Contains(err.Error(), "requires the ephemeral flag") {
		t.Errorf("ephemeral-off-plane: got %v", err)
	}

	// Coherent combinations accepted.
	for _, ok := range []Issue{
		func() Issue { i := validBaseIssue(); i.StorageClass = StorageClassUnversioned; return i }(),
		func() Issue { i := validBaseIssue(); i.StorageClass = StorageClassVersioned; return i }(),
		func() Issue {
			i := validBaseIssue()
			i.Ephemeral = true
			i.StorageClass = StorageClassEphemeral
			return i
		}(),
		func() Issue { i := validBaseIssue(); i.Ephemeral = true; return i }(),
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("coherent issue rejected: %+v: %v", ok.StorageClass, err)
		}
	}
}

// storage_class appears in JSONL for non-versioned records and is omitted when
// versioned (unset).
func TestStorageClassJSONRoundTrip(t *testing.T) {
	unversioned := validBaseIssue()
	unversioned.StorageClass = StorageClassUnversioned
	data, err := json.Marshal(&unversioned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"storage_class":"unversioned"`) {
		t.Errorf("unversioned record should carry storage_class, got: %s", data)
	}

	var back Issue
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.StorageClass != StorageClassUnversioned {
		t.Errorf("round-trip lost storage_class: %q", back.StorageClass)
	}

	versioned := validBaseIssue()
	data, err = json.Marshal(&versioned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "storage_class") {
		t.Errorf("versioned (unset) record must omit storage_class, got: %s", data)
	}
}

func TestPersistenceModeValuesAndValidation(t *testing.T) {
	valid := []PersistenceMode{
		PersistenceModePersistent,
		PersistenceModeEphemeral,
		PersistenceModeNoHistory,
	}
	for _, mode := range valid {
		if !mode.IsValid() {
			t.Errorf("PersistenceMode(%q).IsValid() = false, want true", mode)
		}
	}
	for _, mode := range []PersistenceMode{"", "durable", "wisp"} {
		if mode.IsValid() {
			t.Errorf("PersistenceMode(%q).IsValid() = true, want false", mode)
		}
	}
}

func TestNormalizePersistenceMode(t *testing.T) {
	cases := []struct {
		current                      Issue
		mode                         PersistenceMode
		wantEphemeral, wantNoHistory bool
		wantStorageClass             StorageClass
	}{
		{Issue{}, PersistenceModePersistent, false, false, ""},
		{Issue{StorageClass: StorageClassVersioned}, PersistenceModePersistent, false, false, StorageClassVersioned},
		{Issue{}, PersistenceModeEphemeral, true, false, ""},
		{Issue{}, PersistenceModeNoHistory, false, true, ""},
		{Issue{StorageClass: StorageClassVersioned}, PersistenceModeEphemeral, true, false, ""},
		{Issue{StorageClass: StorageClassVersioned}, PersistenceModeNoHistory, false, true, ""},
		{Issue{Ephemeral: true, StorageClass: StorageClassEphemeral}, PersistenceModeEphemeral, true, false, StorageClassEphemeral},
		{Issue{NoHistory: true, StorageClass: StorageClassEphemeral}, PersistenceModeNoHistory, false, true, StorageClassEphemeral},
		{Issue{Ephemeral: true, StorageClass: StorageClassEphemeral}, PersistenceModeNoHistory, false, true, StorageClassEphemeral},
		{Issue{NoHistory: true, StorageClass: StorageClassEphemeral}, PersistenceModeEphemeral, true, false, StorageClassEphemeral},
		{Issue{Ephemeral: true, StorageClass: StorageClassEphemeral}, PersistenceModePersistent, false, false, ""},
		{Issue{NoHistory: true, StorageClass: StorageClassEphemeral}, PersistenceModePersistent, false, false, ""},
		{Issue{StorageClass: StorageClassUnversioned}, PersistenceModePersistent, false, false, StorageClassUnversioned},
		{Issue{Ephemeral: true}, PersistenceModeEphemeral, true, false, ""},
		{Issue{NoHistory: true}, PersistenceModeNoHistory, false, true, ""},
		{Issue{Ephemeral: true}, PersistenceModeNoHistory, false, true, ""},
		{Issue{NoHistory: true}, PersistenceModeEphemeral, true, false, ""},
	}
	for _, tc := range cases {
		ephemeral, noHistory, storageClass, err := NormalizePersistenceMode(tc.current, tc.mode)
		if err != nil {
			t.Errorf("NormalizePersistenceMode(%q): %v", tc.mode, err)
			continue
		}
		if ephemeral != tc.wantEphemeral || noHistory != tc.wantNoHistory || storageClass != tc.wantStorageClass {
			t.Errorf("NormalizePersistenceMode(%q) = (%t, %t, %q), want (%t, %t, %q)", tc.mode, ephemeral, noHistory, storageClass, tc.wantEphemeral, tc.wantNoHistory, tc.wantStorageClass)
		}
	}
}

func TestNormalizePersistenceModeReturnsValidIssueStates(t *testing.T) {
	for _, target := range []PersistenceMode{PersistenceModeEphemeral, PersistenceModeNoHistory} {
		current := validBaseIssue()
		current.StorageClass = StorageClassVersioned

		ephemeral, noHistory, storageClass, err := NormalizePersistenceMode(current, target)
		if err != nil {
			t.Fatalf("NormalizePersistenceMode(versioned, %q): %v", target, err)
		}
		current.Ephemeral = ephemeral
		current.NoHistory = noHistory
		current.StorageClass = storageClass
		if err := current.Validate(); err != nil {
			t.Errorf("normalized versioned -> %q state is invalid: %v", target, err)
		}
	}

	for _, current := range []Issue{
		func() Issue {
			issue := validBaseIssue()
			issue.Ephemeral = true
			issue.StorageClass = StorageClassEphemeral
			return issue
		}(),
		func() Issue {
			issue := validBaseIssue()
			issue.NoHistory = true
			issue.StorageClass = StorageClassEphemeral
			return issue
		}(),
	} {
		ephemeral, noHistory, storageClass, err := NormalizePersistenceMode(current, PersistenceModePersistent)
		if err != nil {
			t.Fatalf("NormalizePersistenceMode(wisp, persistent): %v", err)
		}
		current.Ephemeral = ephemeral
		current.NoHistory = noHistory
		current.StorageClass = storageClass
		if err := current.Validate(); err != nil {
			t.Errorf("normalized wisp promotion is invalid: %v", err)
		}
	}
}

func TestNormalizePersistenceModeRejectsIncoherentWispFlags(t *testing.T) {
	current := Issue{Ephemeral: true, NoHistory: true}
	for _, target := range []PersistenceMode{
		PersistenceModePersistent,
		PersistenceModeEphemeral,
		PersistenceModeNoHistory,
	} {
		if _, _, _, err := NormalizePersistenceMode(current, target); err == nil {
			t.Errorf("NormalizePersistenceMode(incoherent, %q) succeeded, want error", target)
		}
	}
}

func TestNormalizePersistenceModeRejectsPlaneClassIncoherence(t *testing.T) {
	cases := []struct {
		name    string
		current Issue
		target  PersistenceMode
	}{
		{"ephemeral with explicit versioned", Issue{Ephemeral: true, StorageClass: StorageClassVersioned}, PersistenceModeEphemeral},
		{"no history with explicit versioned", Issue{NoHistory: true, StorageClass: StorageClassVersioned}, PersistenceModeNoHistory},
		{"ephemeral with unversioned", Issue{Ephemeral: true, StorageClass: StorageClassUnversioned}, PersistenceModeEphemeral},
		{"no history with unversioned", Issue{NoHistory: true, StorageClass: StorageClassUnversioned}, PersistenceModeNoHistory},
		{"durable with explicit ephemeral", Issue{StorageClass: StorageClassEphemeral}, PersistenceModePersistent},
	}
	for _, tc := range cases {
		if _, _, _, err := NormalizePersistenceMode(tc.current, tc.target); err == nil {
			t.Errorf("%s: NormalizePersistenceMode succeeded, want error", tc.name)
		}
	}
}

func TestNormalizePersistenceModeRejectsUnsetAndUnknown(t *testing.T) {
	for _, mode := range []PersistenceMode{"", "durable"} {
		if _, _, _, err := NormalizePersistenceMode(Issue{}, mode); err == nil {
			t.Errorf("NormalizePersistenceMode(%q) succeeded, want error", mode)
		}
	}
}

func TestNormalizePersistenceModeRejectsUnversionedDemotion(t *testing.T) {
	current := Issue{StorageClass: StorageClassUnversioned}
	for _, target := range []PersistenceMode{PersistenceModeEphemeral, PersistenceModeNoHistory} {
		if _, _, _, err := NormalizePersistenceMode(current, target); err == nil {
			t.Errorf("NormalizePersistenceMode(unversioned, %q) succeeded, want error", target)
		}
	}
}
