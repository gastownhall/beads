package workapi

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The refusals and the normalization are pinned here, without a database,
// because they are the same on every backend by construction: all three
// implementations validate through ValidateBootstrapRequest and decide the
// refusal through RefuseIdentifiedSubstrate. The conformance contract is left
// to assert what only a real backend can show.

func validBootstrapRequest() issueops.BootstrapRequest {
	return issueops.BootstrapRequest{
		Prefix:    "acme",
		ProjectID: "11111111-2222-3333-4444-555555555555",
	}
}

func TestValidateBootstrapRequestRefusesEachRequiredField(t *testing.T) {
	for _, test := range []struct {
		name  string
		spoil func(*issueops.BootstrapRequest)
	}{
		{"no prefix", func(r *issueops.BootstrapRequest) { r.Prefix = "" }},
		// A prefix that is nothing but the hyphen the plane strips is the
		// interesting empty: it arrives non-empty and normalizes to nothing.
		{"prefix of hyphens", func(r *issueops.BootstrapRequest) { r.Prefix = "---" }},
		{"no project id", func(r *issueops.BootstrapRequest) { r.ProjectID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := validBootstrapRequest()
			test.spoil(&req)
			if _, err := ValidateBootstrapRequest(req); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("ValidateBootstrapRequest() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestValidateBootstrapRequestNormalizesOnlyTheTrailingHyphen(t *testing.T) {
	req := validBootstrapRequest()
	req.Prefix = "my-proj--"
	got, err := ValidateBootstrapRequest(req)
	if err != nil {
		t.Fatalf("ValidateBootstrapRequest(): %v", err)
	}
	// Interior hyphens survive; only the trailing run goes, which is what the
	// settings plane has always done to this key.
	if got.Prefix != "my-proj" {
		t.Fatalf("normalized prefix = %q, want %q", got.Prefix, "my-proj")
	}
	if got.ProjectID != req.ProjectID {
		t.Fatalf("ValidateBootstrapRequest() changed a field it does not own: %+v", got)
	}
}

func TestRefuseIdentifiedSubstrate(t *testing.T) {
	if err := RefuseIdentifiedSubstrate("", ""); err != nil {
		t.Fatalf("RefuseIdentifiedSubstrate(\"\", \"\") = %v, want nil for a substrate with no identity", err)
	}

	// EITHER marker refuses, including the half-identified states a bootstrap
	// that failed partway leaves on a backend with no transaction to roll back.
	for _, test := range []struct{ prefix, projectID string }{
		{"acme", "proj-1"},
		{"acme", ""},
		{"", "proj-1"},
	} {
		err := RefuseIdentifiedSubstrate(test.prefix, test.projectID)
		if !errors.Is(err, issueops.ErrAlreadyIdentified) {
			t.Fatalf("RefuseIdentifiedSubstrate(%q, %q) = %v, want ErrAlreadyIdentified", test.prefix, test.projectID, err)
		}
		var refusal *issueops.AlreadyIdentifiedError
		if !errors.As(err, &refusal) {
			t.Fatalf("RefuseIdentifiedSubstrate(%q, %q) error is not *AlreadyIdentifiedError", test.prefix, test.projectID)
		}
		if refusal.Prefix != test.prefix || refusal.ProjectID != test.projectID {
			t.Fatalf("refusal = %+v, want it to name what was found (%q, %q)", refusal, test.prefix, test.projectID)
		}
	}
}
