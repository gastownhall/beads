package storage

import "testing"

// TestValidateRemoteOperand pins the shared leading-dash rejection
// (GH#7876): option-shaped names are refused verbatim, while the empty name
// (default-remote resolution is the caller's business) and every legal
// spelling — including numeric-leading names like "1st", which cobra used to
// silently misroute (GH#3720) — stay valid.
func TestValidateRemoteOperand(t *testing.T) {
	cases := []struct {
		name    string
		operand string
		wantErr string
	}{
		{name: "dash", operand: "-", wantErr: `invalid remote name "-": must not start with '-'`},
		{name: "short_option", operand: "-f", wantErr: `invalid remote name "-f": must not start with '-'`},
		{name: "long_option", operand: "--force", wantErr: `invalid remote name "--force": must not start with '-'`},
		{name: "option_like_word", operand: "--force-like", wantErr: `invalid remote name "--force-like": must not start with '-'`},
		{name: "empty_is_callers_business", operand: ""},
		{name: "origin", operand: "origin"},
		{name: "numeric_leading", operand: "1st"},
		{name: "peer_with_dashes_inside", operand: "town-beta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRemoteOperand(tc.operand)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateRemoteOperand(%q) = %v, want nil", tc.operand, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateRemoteOperand(%q) = nil, want %q", tc.operand, tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("ValidateRemoteOperand(%q) = %q, want %q", tc.operand, err.Error(), tc.wantErr)
			}
		})
	}
}
