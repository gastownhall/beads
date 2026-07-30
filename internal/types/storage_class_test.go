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

// C2.4: storage_class appears in JSONL for non-versioned records and is
// omitted when versioned (unset).
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
