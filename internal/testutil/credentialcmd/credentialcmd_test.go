package credentialcmd

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtocol(t *testing.T) {
	t.Run("emit preserves exact bytes", func(t *testing.T) {
		payload := []byte("token without newline")
		var stdout, stderr bytes.Buffer
		if code := runProtocol([]string{"emit", base64.RawURLEncoding.EncodeToString(payload)}, &stdout, &stderr); code != 0 {
			t.Fatalf("emit exit = %d, stderr=%q", code, stderr.String())
		}
		if !bytes.Equal(stdout.Bytes(), payload) {
			t.Fatalf("emit stdout = %q, want exact %q", stdout.Bytes(), payload)
		}
	})

	t.Run("exit23 is distinctive", func(t *testing.T) {
		if code := runProtocol([]string{"exit23"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 23 {
			t.Fatalf("exit23 exit = %d, want 23", code)
		}
	})

	t.Run("marker decodes a spaced path", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "marker path with spaces", "invoked")
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		encoded := base64.RawURLEncoding.EncodeToString([]byte(marker))
		var stderr bytes.Buffer
		if code := runProtocol([]string{"marker", encoded}, &bytes.Buffer{}, &stderr); code != 0 {
			t.Fatalf("marker exit = %d, stderr=%q", code, stderr.String())
		}
		got, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "invoked" {
			t.Fatalf("marker content = %q, want invoked", got)
		}
	})

	t.Run("malformed protocol exits 97", func(t *testing.T) {
		var stderr bytes.Buffer
		if code := runProtocol([]string{"emit", "not+rawurl"}, &bytes.Buffer{}, &stderr); code != malformedExit {
			t.Fatalf("malformed exit = %d, want %d", code, malformedExit)
		}
		if !strings.Contains(stderr.String(), "malformed protocol") {
			t.Fatalf("malformed stderr = %q", stderr.String())
		}
	})
}
