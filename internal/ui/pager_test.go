package ui

import (
	"os"
	"strings"
	"testing"
)

func TestContentHeight(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "empty string",
			content: "",
			want:    0,
		},
		{
			name:    "single line",
			content: "hello",
			want:    1,
		},
		{
			name:    "single line with newline",
			content: "hello\n",
			want:    2,
		},
		{
			name:    "multiple lines",
			content: "line1\nline2\nline3",
			want:    3,
		},
		{
			name:    "multiple lines with trailing newline",
			content: "line1\nline2\nline3\n",
			want:    4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contentHeight(tt.content)
			if got != tt.want {
				t.Errorf("contentHeight() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestShouldUsePager(t *testing.T) {
	tests := []struct {
		name      string
		opts      PagerOptions
		envVars   map[string]string
		wantPager bool
	}{
		{
			name:      "NoPager option set",
			opts:      PagerOptions{NoPager: true},
			wantPager: false,
		},
		{
			name:      "BD_NO_PAGER env set",
			opts:      PagerOptions{NoPager: false},
			envVars:   map[string]string{"BD_NO_PAGER": "1"},
			wantPager: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up env vars
			for k, v := range tt.envVars {
				oldVal := os.Getenv(k)
				os.Setenv(k, v)
				defer os.Setenv(k, oldVal)
			}

			got := shouldUsePager(tt.opts)
			if got != tt.wantPager {
				t.Errorf("shouldUsePager() = %v, want %v", got, tt.wantPager)
			}
		})
	}
}

func TestGetPagerCommand(t *testing.T) {
	tests := []struct {
		name      string
		envVars   map[string]string
		wantPager string
	}{
		{
			name:      "default pager",
			envVars:   map[string]string{},
			wantPager: "less",
		},
		{
			name:      "BD_PAGER set",
			envVars:   map[string]string{"BD_PAGER": "more"},
			wantPager: "more",
		},
		{
			name:      "PAGER set",
			envVars:   map[string]string{"PAGER": "cat"},
			wantPager: "cat",
		},
		{
			name:      "BD_PAGER takes precedence over PAGER",
			envVars:   map[string]string{"BD_PAGER": "more", "PAGER": "cat"},
			wantPager: "more",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and clear relevant env vars
			oldBdPager := os.Getenv("BD_PAGER")
			oldPager := os.Getenv("PAGER")
			os.Unsetenv("BD_PAGER")
			os.Unsetenv("PAGER")
			defer func() {
				if oldBdPager != "" {
					os.Setenv("BD_PAGER", oldBdPager)
				}
				if oldPager != "" {
					os.Setenv("PAGER", oldPager)
				}
			}()

			// Set up env vars for test
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			got := getPagerCommand()
			if got != tt.wantPager {
				t.Errorf("getPagerCommand() = %q, want %q", got, tt.wantPager)
			}
		})
	}
}

func TestToPagerNoPagerOption(t *testing.T) {
	// Create a test output that we want to capture
	content := "test content\n"

	// With NoPager=true, ToPager should just print directly
	// (we can't easily capture stdout in a test, but we can verify no error)
	err := ToPager(content, PagerOptions{NoPager: true})
	if err != nil {
		t.Errorf("ToPager() returned error: %v", err)
	}
}

func TestToPagerWithBdNoPagerEnv(t *testing.T) {
	oldVal := os.Getenv("BD_NO_PAGER")
	os.Setenv("BD_NO_PAGER", "1")
	defer func() {
		if oldVal != "" {
			os.Setenv("BD_NO_PAGER", oldVal)
		} else {
			os.Unsetenv("BD_NO_PAGER")
		}
	}()

	content := strings.Repeat("line\n", 100)
	err := ToPager(content, PagerOptions{})
	if err != nil {
		t.Errorf("ToPager() returned error: %v", err)
	}
}
