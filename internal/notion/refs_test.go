package notion

import "testing"

func TestCanonicalizeNotionExternalRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  string
		want string
		ok   bool
	}{
		{
			name: "canonical url",
			ref:  "https://www.notion.so/My-Page-0123456789abcdef0123456789abcdef?pvs=4",
			want: "https://www.notion.so/0123456789abcdef0123456789abcdef",
			ok:   true,
		},
		{
			name: "bare page id",
			ref:  "0123456789abcdef0123456789abcdef",
			want: "notion:01234567-89ab-cdef-0123-456789abcdef",
			ok:   true,
		},
		{
			name: "prefixed page id",
			ref:  "notion:01234567-89ab-cdef-0123-456789abcdef",
			want: "notion:01234567-89ab-cdef-0123-456789abcdef",
			ok:   true,
		},
		{
			name: "not notion",
			ref:  "https://example.com/item/1",
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CanonicalizeNotionExternalRef(tt.ref)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("got = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractNotionIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref  string
		want string
	}{
		{
			ref:  "https://www.notion.so/My-Page-0123456789abcdef0123456789abcdef",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
		{
			ref:  "0123456789abcdef0123456789abcdef",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
		{
			ref:  "notion:01234567-89ab-cdef-0123-456789abcdef",
			want: "01234567-89ab-cdef-0123-456789abcdef",
		},
	}

	for _, tt := range tests {
		if got := ExtractNotionIdentifier(tt.ref); got != tt.want {
			t.Fatalf("ExtractNotionIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestBuildNotionExternalRefPrefersURL(t *testing.T) {
	t.Parallel()

	issue := &PulledIssue{
		ExternalRef:  "https://www.notion.so/Test-0123456789abcdef0123456789abcdef",
		NotionPageID: "fedcba98-7654-3210-fedc-ba9876543210",
	}

	got := BuildNotionExternalRef(issue)
	want := "https://www.notion.so/0123456789abcdef0123456789abcdef"
	if got != want {
		t.Fatalf("got = %q, want %q", got, want)
	}
}
