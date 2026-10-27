//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestStoreReadCommandHintsQuoteIDs is the behavioral half of
// TestRemedyCensusStoreIDHints for the hints PR #7868 left that now route
// through remedy.CommandArg: bd find-duplicates'
// Compare line, bd mol ready --gated's bd assign line, bd show's
// comments-tail and raw-edge pointers, bd dep list's raw-edge pointer, bd dep
// add's malformed-target refusal and bd close's unserved auto-close note. Each reads an issue ID from the store,
// which holds a collaborator's issues after bd dolt pull. An ID holding shell
// syntax goes into the suggested command single-quoted on POSIX, and one no
// quoting passes on (a carriage return and terminal escape) takes a
// placeholder named escaped after the command; no ID reaches the terminal
// raw, so pasting the command runs no part of it and none can redraw it. An
// ordinary ID prints as it did.
func TestStoreReadCommandHintsQuoteIDs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the rows spell the POSIX quoting")
	}
	initConfigForTest(t)

	fmtTime := func(tm time.Time) string { return tm.UTC().Format("2006-01-02 15:04") }

	// Each printer returns the single line that carries its bd command, given
	// the store-read ID.
	bdLine := func(t *testing.T, out, verb string) string {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, verb) {
				return line
			}
		}
		t.Fatalf("no %q line in output:\n%q", verb, out)
		return ""
	}

	sites := []struct {
		name, verb string
		// hint returns the whole output and the bd-command line for an ID.
		hint func(t *testing.T, id string) (out, line string)
	}{
		{"bd find-duplicates", "bd show", func(t *testing.T, id string) (string, string) {
			issues := []*types.Issue{
				{ID: id, Title: "Same bug here", Description: "one identical body of text", Status: types.StatusOpen},
				{ID: "bd-fixed", Title: "Same bug here", Description: "one identical body of text", Status: types.StatusOpen},
			}
			out := captureStdout(t, func() error {
				return reportFindDuplicates(context.Background(), issues, "mechanical", 0.1, 0, "")
			})
			return out, bdLine(t, out, "Compare:")
		}},
		{"bd mol ready --gated", "bd assign", func(t *testing.T, id string) (string, string) {
			out := captureStdout(t, func() error {
				return renderGatedReadyMolecules([]*GatedMolecule{{
					MoleculeID: "bd-mol", MoleculeTitle: "Mol",
					ReadyStep: &types.Issue{ID: id, Title: "Step"},
				}})
			})
			return out, bdLine(t, out, "bd assign")
		}},
		{"bd show comments-tail", "bd show", func(t *testing.T, id string) (string, string) {
			out := captureStdout(t, func() error {
				printComments([]*types.Comment{
					{Author: "a", Text: "first", CreatedAt: time.Unix(0, 0)},
					{Author: "a", Text: "second", CreatedAt: time.Unix(0, 0)},
				}, 1, fmtTime, id)
				return nil
			})
			return out, bdLine(t, out, "older")
		}},
		{"bd show raw-edge pointer", "bd dep list", func(t *testing.T, id string) (string, string) {
			out := captureStderr(t, func() {
				warnUnresolvableDepEdges(id, depCounts{deps: depCount{n: 1}}, depListing{rows: 0}, depListing{})
			})
			return out, bdLine(t, out, "bd dep list")
		}},
		{"bd dep list raw-edge pointer", "bd dep list", func(t *testing.T, id string) (string, string) {
			reader := fakeAnchorEdgeReader{edges: []*types.Dependency{
				{IssueID: id, DependsOnID: id + "-far", Type: types.DepBlocks},
			}}
			out := captureStderr(t, func() {
				warnDroppedDepEdges(context.Background(), reader, id, "", nil)
			})
			return out, bdLine(t, out, "bd dep list")
		}},
		{"bd dep add malformed-target refusal", "bd dep add", func(t *testing.T, id string) (string, string) {
			err := refuseMalformedDepTarget(id, "discovered-from:bd-tgt")
			if err == nil {
				t.Fatal("refuseMalformedDepTarget accepted a <type>:<id> target")
			}
			return err.Error(), bdLine(t, err.Error(), "bd dep add")
		}},
		{"bd close unserved auto-close note", "bd close", func(t *testing.T, id string) (string, string) {
			s := newUnservedMoleculeStore(id, "bd-step")
			out := captureStderr(t, func() {
				noticeUnservedMoleculeAutoClose(context.Background(), s, "bd-step", nil)
			})
			return out, bdLine(t, out, "bd close")
		}},
	}

	// value is an ID a collaborator's issue can carry after a pull; posix is
	// how ShellQuotedFor spells it for the command on POSIX, quoted when it
	// prints as itself and refused (a placeholder) otherwise.
	for _, id := range []struct {
		value string
	}{
		{"bd-ok"},
		{"bd-x$(touch${IFS}pwned)"},
		{"bd-y`;touch pwned;`"},
		{hostileName},
	} {
		quoted, canQuote := remedy.ShellQuotedFor(runtime.GOOS, id.value)
		for _, site := range sites {
			t.Run(site.name+"/"+id.value, func(t *testing.T) {
				out, line := site.hint(t, id.value)
				// No line of the output, the bd-command line or a line beside
				// it that names the ID, reaches the terminal raw: none can
				// redraw the command.
				requireNoTerminalControl(t, site.name, out)
				// A line beside the command names a non-plain ID escaped
				// (printedIssueID), so pasting it with the command runs no
				// part of the ID either.
				if id.value != "bd-ok" {
					for _, other := range strings.Split(out, "\n") {
						if other != line && strings.Contains(other, id.value) {
							t.Errorf("%s names a store-read ID raw beside its command:\n%q", site.name, other)
						}
					}
				}
				switch {
				case id.value == "bd-ok":
					// An ordinary ID is pasted bare, as it always was.
					if !strings.Contains(line, " bd-ok") {
						t.Errorf("%s does not name an ordinary ID bare:\n%q", site.name, line)
					}
				case canQuote:
					// A shell-syntax ID that prints as itself is single-quoted,
					// so pasting the command runs no part of it.
					if !strings.Contains(line, quoted) {
						t.Errorf("%s does not single-quote a shell-syntax ID:\n%q\nwant it to hold %q", site.name, line, quoted)
					}
				default:
					// An ID no quoting passes on takes a placeholder; its raw
					// value never reaches the command line.
					if strings.Contains(line, id.value) {
						t.Errorf("%s pastes an unquotable ID raw:\n%q", site.name, line)
					}
					if !strings.Contains(line, "<") {
						t.Errorf("%s does not take a placeholder for an unquotable ID:\n%q", site.name, line)
					}
				}
			})
		}
	}
}

// fakeAnchorEdgeReader answers every anchor a request names with edges.
type fakeAnchorEdgeReader struct{ edges []*types.Dependency }

func (r fakeAnchorEdgeReader) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	var res issueops.EdgeReadResult
	for _, id := range req.IDs {
		res.Anchors = append(res.Anchors, issueops.AnchorEdges{ID: id, Edges: r.edges})
	}
	return res, nil
}

// unservedMoleculeStore is a store whose server does not advertise
// issues.close.autoCloseMolecule (storage.MoleculeAutoCloseProber answers
// false), holding one molecule root with one closed step, or answering every
// molecule read with readErr when it is set, or only the read of the root's
// steps with relatedErr.
type unservedMoleculeStore struct {
	storage.DoltStorage
	root, step *types.Issue
	readErr    error
	relatedErr error
}

func newUnservedMoleculeStore(rootID, stepID string) *unservedMoleculeStore {
	return &unservedMoleculeStore{
		root: &types.Issue{ID: rootID, Title: "Mol", IssueType: types.TypeMolecule, Status: types.StatusOpen},
		step: &types.Issue{ID: stepID, Title: "Step", IssueType: types.TypeTask, Status: types.StatusClosed},
	}
}

func (s *unservedMoleculeStore) ServesMoleculeAutoClose(context.Context) (bool, error) {
	return false, nil
}

func (s *unservedMoleculeStore) BatchGetter() (issueops.BatchGetter, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s, nil
}
func (s *unservedMoleculeStore) IssueRelations() (issueops.Relations, error) { return s, nil }
func (s *unservedMoleculeStore) EdgeReader() (issueops.EdgeReader, error)    { return s, nil }

func (s *unservedMoleculeStore) GetMany(_ context.Context, req issueops.GetManyRequest) (issueops.GetManyResult, error) {
	var res issueops.GetManyResult
	for _, id := range req.IDs {
		for _, iss := range []*types.Issue{s.root, s.step} {
			if iss.ID == id {
				res.Issues = append(res.Issues, iss)
			}
		}
	}
	return res, nil
}

func (s *unservedMoleculeStore) ReadEdges(_ context.Context, req issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	var res issueops.EdgeReadResult
	for _, id := range req.IDs {
		anchor := issueops.AnchorEdges{ID: id}
		if id == s.step.ID {
			anchor.Edges = []*types.Dependency{{IssueID: s.step.ID, DependsOnID: s.root.ID, Type: types.DepParentChild}}
		}
		res.Anchors = append(res.Anchors, anchor)
	}
	return res, nil
}

func (s *unservedMoleculeStore) Related(_ context.Context, req issueops.RelatedRequest) ([]*issueops.RelatedIssue, error) {
	if req.ID != s.root.ID || req.Direction != issueops.RelationIn {
		return nil, nil
	}
	if s.relatedErr != nil {
		return nil, s.relatedErr
	}
	return []*issueops.RelatedIssue{{Issue: *s.step, DependencyType: types.DepParentChild}}, nil
}

// TestUnservedMoleculeAutoCloseNoteNamesIDsEscaped pins the lines of bd
// close's note on a molecule a close left complete against a server that does
// not advertise issues.close.autoCloseMolecule: the step and root IDs, read
// from the store, are named as printedIssueID spells them, on the line naming
// the bd close command and on the note that the molecule could not be read,
// and an ordinary ID prints as it did.
func TestUnservedMoleculeAutoCloseNoteNamesIDsEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the rows spell the POSIX quoting")
	}
	t.Run("ordinary", func(t *testing.T) {
		out := captureStderr(t, func() {
			noticeUnservedMoleculeAutoClose(context.Background(), newUnservedMoleculeStore("bd-mol", "bd-step"), "bd-step", nil)
		})
		want := "note: closing bd-step completed molecule bd-mol, which this close did not auto-close: the server does not advertise issues.close.autoCloseMolecule, so the molecule's auto-close is the server's to run (bd close bd-mol closes it)\n"
		if out != want {
			t.Errorf("note = %q\nwant %q", out, want)
		}
	})
	t.Run("hostile step", func(t *testing.T) {
		s := newUnservedMoleculeStore("bd-mol", hostileName)
		out := captureStderr(t, func() {
			noticeUnservedMoleculeAutoClose(context.Background(), s, hostileName, nil)
		})
		requireNoTerminalControl(t, "unserved auto-close note", out)
		if !strings.Contains(out, printedIssueID(hostileName)) || !strings.Contains(out, "(bd close bd-mol closes it)") {
			t.Errorf("note does not name the step escaped beside an ordinary root:\n%q", out)
		}
	})
	t.Run("unreadable molecule", func(t *testing.T) {
		for _, id := range []string{"bd-step", hostileName, "bd-x$(touch${IFS}pwned)"} {
			s := newUnservedMoleculeStore("bd-mol", id)
			s.readErr = errors.New("read failed")
			out := captureStderr(t, func() {
				noticeUnservedMoleculeAutoClose(context.Background(), s, id, nil)
			})
			requireNoTerminalControl(t, "unserved auto-close note", out)
			want := "note: " + printedIssueID(id) + " was closed without its molecule auto-close"
			if !strings.HasPrefix(out, want) {
				t.Errorf("note = %q\nwant it to start %q", out, want)
			}
			if id != "bd-step" && strings.Contains(out, id) {
				t.Errorf("note names a non-plain step ID raw:\n%q", out)
			}
		}
	})
	// A failed read of the molecule's steps names the root in its error
	// ("reading the steps of <root>"), so the note spells that error by
	// remedy.PrintedError rather than printing it raw.
	t.Run("unreadable steps of a hostile root", func(t *testing.T) {
		s := newUnservedMoleculeStore(hostileName, "bd-step")
		s.relatedErr = errors.New("backend unavailable")
		out := captureStderr(t, func() {
			noticeUnservedMoleculeAutoClose(context.Background(), s, "bd-step", nil)
		})
		requireNoTerminalControl(t, "unserved auto-close note", out)
		requireNamesHostileValue(t, "unserved auto-close note", out)
		if !strings.HasPrefix(out, "note: bd-step was closed without its molecule auto-close") || !strings.Contains(out, "backend unavailable") {
			t.Errorf("note does not report the failed read:\n%q", out)
		}
	})
}

// TestAgentDiagnosticCommandsKeepBackticksClosed pins the shape of bd doctor
// --agent's "To fix" list: every command is shown as a code span. One
// without a backtick gets the single-backtick span it always had. One that
// holds a backtick gets an even fence longer than its longest backtick run,
// so the backtick cannot close the span (CommonMark), padded with a space
// when the command starts or ends with one: a check's Fix can carry a value
// remedy.CommandArg single-quoted from a committed file name, and a span its
// own backtick closed would break apart. TestAgentDiagnosticSpansPasteAsTheirCommand
// pastes such spans.
func TestAgentDiagnosticCommandsKeepBackticksClosed(t *testing.T) {
	ordinary := "bd doctor --fix"
	hostile, _ := remedy.CommandArg("linux", "/repo/.githooks/pre-commit`;touch pwned;`.legacy", "<path>")
	hostileCmd := "rm " + hostile
	doubled := "echo 'a``b'"
	edged := "`x` then y"
	out := captureStdout(t, func() error {
		printAgentDiagnostic(doctor.AgentDiagnostic{
			Name: "Stale Legacy Hooks", Status: statusWarning, Severity: "degraded",
			Commands: []string{ordinary, hostileCmd, doubled, edged},
		})
		return nil
	})
	for _, want := range []string{
		"  - `" + ordinary + "`\n",
		"  - ``" + hostileCmd + "``\n",
		"  - ````" + doubled + "````\n",
		"  - `` " + edged + " ``\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("To fix list lacks %q:\n%s", want, out)
		}
	}
}

// TestAgentDiagnosticBlockedGateAdviceStaysOneSpan runs bd's own advice
// through the blocked fix gate (GH#4993) and pastes the span bd doctor
// --agent prints for it. The gate's Reason names the bd doctor --fix it
// withholds in single quotes, as AssessSchemaFixGate writes each Reason
// (cmd/bd/doctor's assertAgentAdviceHoldsNoBacktick pins the real ones), so
// the advice holds no backtick: it prints as one single-backtick span, and
// pasting that span runs no bd. Main quoted the command in backticks there,
// and pasting main's span into sh or bash ran bd doctor --fix.
func TestAgentDiagnosticBlockedGateAdviceStaysOneSpan(t *testing.T) {
	gate := doctor.FixGate{
		Determined: true, DBReachable: true, DBPresent: true, Pending: true,
		DBVersion: 1, BinaryVersion: 2, AllowFSFix: true,
		Reason: "database schema is at v1, this binary expects v2 (1 migration pending). " +
			"'bd doctor --fix' may apply write migrations — upgrade/migrate deliberately, not as a cosmetic tip",
	}
	ad := doctor.EnrichForAgent(doctor.DoctorCheck{
		Name: "Permissions", Status: statusWarning, Message: "loose", Fix: "bd doctor --fix",
	}, gate)
	if len(ad.Commands) == 0 {
		t.Fatalf("no commands enriched: %+v", ad)
	}
	out := captureStdout(t, func() error { printAgentDiagnostic(ad); return nil })
	var spans []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "  - ") || !strings.Contains(line, "Original tip was") {
			continue
		}
		span := strings.TrimPrefix(line, "  - ")
		spans = append(spans, span)
		if !strings.HasPrefix(span, "`Avoid ") || !strings.HasSuffix(span, "`") || strings.Count(span, "`") != 2 {
			t.Errorf("gated advice is not one single-backtick span: %q", line)
		}
	}
	if len(spans) == 0 {
		t.Fatalf("no gated advice in:\n%s", out)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, span := range spans {
		pasteLine(t, span, func(t *testing.T, _ string, left []string) {
			if len(left) != 0 {
				t.Errorf("pasting the gated advice ran something, leaving %q:\n%s", left, span)
			}
		})
	}
}

// TestAgentDiagnosticSpansPasteAsTheirCommand pastes the even-fenced spans
// bd doctor --agent prints for commands whose value, single-quoted by
// remedy.CommandArg, holds backticks: in sh, bash and zsh the fence pastes
// as empty command substitutions, so each span runs its command as written,
// passing the value on unchanged, and no part of the value runs.
func TestAgentDiagnosticSpansPasteAsTheirCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pastes into POSIX shells")
	}
	values := []struct{ name, value string }{
		{"backtick", "/repo/.githooks/pre-commit`;touch pwned;`.legacy"},
		{"backtick pair", "a``b`touch pwned`c"},
		{"backtick triple", "x```touch pwned```y"},
	}
	var cmds []string
	for _, v := range values {
		arg, note := remedy.CommandArg(runtime.GOOS, v.value, "<path>")
		if note != "" {
			t.Fatalf("remedy.CommandArg left %q out of the command: %s", v.value, note)
		}
		cmds = append(cmds, "printf '%s\\n' "+arg)
	}
	out := captureStdout(t, func() error {
		printAgentDiagnostic(doctor.AgentDiagnostic{
			Name: "Stale Legacy Hooks", Status: statusWarning, Severity: "degraded", Commands: cmds,
		})
		return nil
	})
	var spans []string
	for _, line := range strings.Split(out, "\n") {
		if span, ok := strings.CutPrefix(line, "  - "); ok {
			spans = append(spans, span)
		}
	}
	if len(spans) != len(values) {
		t.Fatalf("To fix list has %d spans, want %d:\n%s", len(spans), len(values), out)
	}
	for i, span := range spans {
		t.Run(values[i].name, func(t *testing.T) {
			if !strings.HasPrefix(span, "``") {
				t.Fatalf("span %q is not fenced by a run of backticks", span)
			}
			pasteLine(t, span, func(t *testing.T, stdout string, left []string) {
				if stdout != values[i].value+"\n" {
					t.Errorf("pasting %s printed %q, want the value %q", span, stdout, values[i].value)
				}
				if len(left) != 0 {
					t.Errorf("pasting %s ran part of the value, leaving %q", span, left)
				}
			})
		})
	}
}

// pasteLine runs line as sh, bash and zsh run a line pasted into them, each
// that is installed, with no startup files, in a fresh directory and with a
// stub bd first on PATH that leaves a bd.ran file there. It calls check with
// the shell's stdout and the names of the files the paste left in the
// directory.
func pasteLine(t *testing.T, line string, check func(t *testing.T, stdout string, left []string)) {
	t.Helper()
	pasted := 0
	for _, shell := range []struct {
		name string
		args []string
	}{
		{"sh", nil},
		{"bash", []string{"--noprofile", "--norc"}},
		{"zsh", []string{"-f"}},
	} {
		path, err := exec.LookPath(shell.name)
		if err != nil {
			continue
		}
		pasted++
		t.Run(shell.name, func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			stub := "#!/bin/sh\necho \"$@\" >> bd.ran\n"
			if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(path, append(shell.args, "-c", line)...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			// A line the shell cannot parse runs nothing, so only what it
			// printed and the files it left are evidence.
			stdout, _ := cmd.Output()
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var left []string
			for _, entry := range entries {
				left = append(left, entry.Name())
			}
			check(t, string(stdout), left)
		})
	}
	if pasted == 0 {
		t.Skip("no POSIX shell to paste into")
	}
}

// TestStoreReadHintsNameProseAsRecorded pins the prose bd find-duplicates
// and bd mol ready --gated print beside their paste-ready commands: the
// titles, the gate's await type and find-duplicates' AI reason are spelled
// by remedy.PrintedValue, as bd prints a title in every listing. One holding
// shell syntax is named as recorded and never reaches the command line, and
// a line break or terminal control in one is escaped, so it cannot add a
// line that reads as a command or redraw the one there is.
func TestStoreReadHintsNameProseAsRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command lines spell the POSIX quoting")
	}
	initConfigForTest(t)
	sites := []struct {
		name, command string
		print         func(t *testing.T, prose string) string
	}{
		{"bd find-duplicates", "Compare: bd show bd-1 bd-2", func(t *testing.T, prose string) string {
			return captureStdout(t, func() error {
				printDuplicatePair(1, duplicatePair{
					IssueA: &types.Issue{ID: "bd-1", Title: prose}, IssueB: &types.Issue{ID: "bd-2", Title: "Other"},
					Similarity: 0.9, Method: "ai", Reason: prose,
				})
				return nil
			})
		}},
		{"bd mol ready --gated", "bd assign bd-1.1 <agent>", func(t *testing.T, prose string) string {
			return captureStdout(t, func() error {
				return renderGatedReadyMolecules([]*GatedMolecule{{
					MoleculeID: "bd-1", MoleculeTitle: prose,
					ClosedGate: &types.Issue{ID: "bd-1.g", AwaitType: prose},
					ReadyStep:  &types.Issue{ID: "bd-1.1", Title: prose},
				}})
			})
		}},
	}
	for _, prose := range []struct{ name, value string }{
		{"ordinary", "Crash on close"},
		{"shell syntax", "Fix $(touch pwned) and `id`"},
		{"line breaks", "Same crash.\n  Compare: bd show bd-evil bd-1\n  bd assign bd-evil <agent>"},
		{"terminal control", hostileName},
	} {
		for _, site := range sites {
			t.Run(site.name+"/"+prose.name, func(t *testing.T) {
				out := site.print(t, prose.value)
				requireNoTerminalControl(t, site.name, out)
				if !strings.Contains(out, remedy.PrintedValue(prose.value)) {
					t.Errorf("%s does not name %q as remedy.PrintedValue spells it:\n%s", site.name, prose.value, out)
				}
				var commands []string
				for _, line := range strings.Split(out, "\n") {
					if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "Compare:") || strings.HasPrefix(trimmed, "bd assign") {
						commands = append(commands, trimmed)
					}
				}
				if len(commands) != 1 || commands[0] != site.command {
					t.Errorf("%s command lines = %q, want just %q:\n%s", site.name, commands, site.command, out)
				}
			})
		}
	}
}
