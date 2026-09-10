package doltversion

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
		wantSeg []int
		wantPre string
	}{
		{
			name:    "plain version",
			input:   "dolt version 1.52.3\n",
			wantSeg: []int{1, 52, 3},
		},
		{
			name:    "plain version no trailing newline",
			input:   "dolt version 2.0.0",
			wantSeg: []int{2, 0, 0},
		},
		{
			name:    "prerelease suffix",
			input:   "dolt version 1.2.3-beta1\n",
			wantSeg: []int{1, 2, 3},
			wantPre: "beta1",
		},
		{
			name:    "dev build suffix",
			input:   "dolt version 2.0.0-dev\n",
			wantSeg: []int{2, 0, 0},
			wantPre: "dev",
		},
		{
			name: "multi-line output with storage format line",
			input: "dolt version 1.52.3\n" +
				"database storage format: __LD_1__\n",
			wantSeg: []int{1, 52, 3},
		},
		{
			name:    "empty output",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   \n",
			wantErr: true,
		},
		{
			name:    "garbage output",
			input:   "not a version string at all\n",
			wantErr: true,
		},
		{
			name:    "trailing dash with nothing after",
			input:   "dolt version 1.2.3-\n",
			wantSeg: []int{1, 2, 3},
			wantPre: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := ParseVersion(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want error", tc.input, v)
				}
				if !errors.Is(err, ErrUnparseableVersion) {
					t.Errorf("ParseVersion(%q) error = %v, want wrapping ErrUnparseableVersion", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) unexpected error: %v", tc.input, err)
			}
			if len(v.Segments) != len(tc.wantSeg) {
				t.Fatalf("ParseVersion(%q) segments = %v, want %v", tc.input, v.Segments, tc.wantSeg)
			}
			for i := range tc.wantSeg {
				if v.Segments[i] != tc.wantSeg[i] {
					t.Errorf("ParseVersion(%q) segments = %v, want %v", tc.input, v.Segments, tc.wantSeg)
				}
			}
			if v.Prerelease != tc.wantPre {
				t.Errorf("ParseVersion(%q) prerelease = %q, want %q", tc.input, v.Prerelease, tc.wantPre)
			}
		})
	}
}

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		name string
		v    Version
		min  Version
		want bool
	}{
		{"equal", MustParse("2.0.0"), MustParse("2.0.0"), true},
		{"greater major", MustParse("3.0.0"), MustParse("2.0.0"), true},
		{"lesser major", MustParse("1.99.99"), MustParse("2.0.0"), false},
		{"greater patch", MustParse("2.0.1"), MustParse("2.0.0"), true},
		{"shorter segments treated as zero-padded", mustParseRaw(t, "dolt version 1.52"), mustParseRaw(t, "dolt version 1.52.0"), true},
		{"prerelease ignored for comparison", mustParseRaw(t, "dolt version 2.0.0-beta1"), MustParse("2.0.0"), true},
		{"1.85 below 2.0.0", MustParse("1.85.0"), MustParse("2.0.0"), false},
		{"1.52.1 below 2.0.0", MustParse("1.52.1"), MustParse("2.0.0"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.AtLeast(tc.min); got != tc.want {
				t.Errorf("%v.AtLeast(%v) = %v, want %v", tc.v, tc.min, got, tc.want)
			}
		})
	}
}

func mustParseRaw(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestMustParsePanicsOnBadInput(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustParse did not panic on invalid input")
		}
	}()
	MustParse("not-a-version")
}

func TestVersionString(t *testing.T) {
	if got, want := MustParse("2.0.0").String(), "2.0.0"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	v := mustParseRaw(t, "dolt version 1.2.3-beta1")
	if got, want := v.String(), "1.2.3-beta1"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
