package storage

import "testing"

func TestValidateRemoteOperand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
	}{
		{name: "-", wantErr: `invalid remote name "-": must not start with '-'`},
		{name: "-f", wantErr: `invalid remote name "-f": must not start with '-'`},
		{name: "--force", wantErr: `invalid remote name "--force": must not start with '-'`},
		{name: ""},
		{name: "origin"},
		{name: "1st"},
		{name: "town-beta"},
		{name: "a-"},
	} {
		err := ValidateRemoteOperand(tc.name)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("ValidateRemoteOperand(%q) = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.wantErr {
			t.Errorf("ValidateRemoteOperand(%q) = %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}
