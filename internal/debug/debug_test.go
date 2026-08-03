package debug

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestEnabled(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     bool
	}{
		{"enabled with value", "1", true},
		{"enabled with any value", "true", true},
		{"disabled when empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldEnabled := enabled
			defer func() { enabled = oldEnabled }()

			if tt.envValue != "" {
				enabled = true
			} else {
				enabled = false
			}

			if got := Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLogf(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		format     string
		args       []interface{}
		wantOutput string
	}{
		{
			name:       "outputs when enabled",
			enabled:    true,
			format:     "test message: %s\n",
			args:       []interface{}{"hello"},
			wantOutput: "test message: hello\n",
		},
		{
			name:       "no output when disabled",
			enabled:    false,
			format:     "test message: %s\n",
			args:       []interface{}{"hello"},
			wantOutput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldEnabled := enabled
			oldStderr := os.Stderr
			defer func() {
				enabled = oldEnabled
				os.Stderr = oldStderr
			}()

			enabled = tt.enabled

			r, w, _ := os.Pipe()
			os.Stderr = w

			Logf(tt.format, tt.args...)

			w.Close()
			var buf bytes.Buffer
			io.Copy(&buf, r)

			if got := buf.String(); got != tt.wantOutput {
				t.Errorf("Logf() output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

func TestPrintf(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		format     string
		args       []interface{}
		wantOutput string
	}{
		{
			name:       "outputs when enabled",
			enabled:    true,
			format:     "debug: %d\n",
			args:       []interface{}{42},
			wantOutput: "debug: 42\n",
		},
		{
			name:       "no output when disabled",
			enabled:    false,
			format:     "debug: %d\n",
			args:       []interface{}{42},
			wantOutput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldEnabled := enabled
			oldStdout := os.Stdout
			defer func() {
				enabled = oldEnabled
				os.Stdout = oldStdout
			}()

			enabled = tt.enabled

			r, w, _ := os.Pipe()
			os.Stdout = w

			Printf(tt.format, tt.args...)

			w.Close()
			var buf bytes.Buffer
			io.Copy(&buf, r)

			if got := buf.String(); got != tt.wantOutput {
				t.Errorf("Printf() output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

func TestSetVerbose(t *testing.T) {
	oldVerbose := verboseMode
	oldEnabled := enabled
	defer func() {
		verboseMode = oldVerbose
		enabled = oldEnabled
	}()

	enabled = false
	verboseMode = false

	if Enabled() {
		t.Error("Enabled() should be false initially")
	}

	SetVerbose(true)
	if !Enabled() {
		t.Error("Enabled() should be true after SetVerbose(true)")
	}

	SetVerbose(false)
	if Enabled() {
		t.Error("Enabled() should be false after SetVerbose(false)")
	}
}

func TestSetQuietAndIsQuiet(t *testing.T) {
	oldQuiet := quietMode
	defer func() { quietMode = oldQuiet }()

	quietMode = false

	if IsQuiet() {
		t.Error("IsQuiet() should be false initially")
	}

	SetQuiet(true)
	if !IsQuiet() {
		t.Error("IsQuiet() should be true after SetQuiet(true)")
	}

	SetQuiet(false)
	if IsQuiet() {
		t.Error("IsQuiet() should be false after SetQuiet(false)")
	}
}

func TestPrintNormal(t *testing.T) {
	tests := []struct {
		name       string
		quiet      bool
		format     string
		args       []interface{}
		wantOutput string
	}{
		{
			name:       "outputs when not quiet",
			quiet:      false,
			format:     "info: %s\n",
			args:       []interface{}{"message"},
			wantOutput: "info: message\n",
		},
		{
			name:       "no output when quiet",
			quiet:      true,
			format:     "info: %s\n",
			args:       []interface{}{"message"},
			wantOutput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldQuiet := quietMode
			oldStdout := os.Stdout
			defer func() {
				quietMode = oldQuiet
				os.Stdout = oldStdout
			}()

			quietMode = tt.quiet

			r, w, _ := os.Pipe()
			os.Stdout = w

			PrintNormal(tt.format, tt.args...)

			w.Close()
			var buf bytes.Buffer
			io.Copy(&buf, r)

			if got := buf.String(); got != tt.wantOutput {
				t.Errorf("PrintNormal() output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

func TestPrintlnNormal(t *testing.T) {
	tests := []struct {
		name       string
		quiet      bool
		args       []interface{}
		wantOutput string
	}{
		{
			name:       "outputs when not quiet",
			quiet:      false,
			args:       []interface{}{"hello", "world"},
			wantOutput: "hello world\n",
		},
		{
			name:       "no output when quiet",
			quiet:      true,
			args:       []interface{}{"hello", "world"},
			wantOutput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldQuiet := quietMode
			oldStdout := os.Stdout
			defer func() {
				quietMode = oldQuiet
				os.Stdout = oldStdout
			}()

			quietMode = tt.quiet

			r, w, _ := os.Pipe()
			os.Stdout = w

			PrintlnNormal(tt.args...)

			w.Close()
			var buf bytes.Buffer
			io.Copy(&buf, r)

			if got := buf.String(); got != tt.wantOutput {
				t.Errorf("PrintlnNormal() output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}
