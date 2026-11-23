// Package remedy is the one printer for the values bd names in a remedy: a
// "Run: ..." line, a hint, or a refusal that tells the user what to run, and
// the message lines printed beside one.
//
// The invariant it keeps: a value that comes from config.yaml, the
// filesystem, committed issue data (which travels with bd dolt pull) or user
// input, printed inside a paste-ready command or beside one,
//
//   - goes inside the command only quoted for the shells people paste it
//     into on the target platform (ShellQuotedFor, ShellWord, CommandArg),
//   - is named with no character a terminal acts on, so it cannot redraw,
//     reorder or split the line that tells the user what to run
//     (PrintedValue, PrintedList, and PrintedError for an error bd did not
//     compose that repeats it, such as a dial error naming a host), and
//   - when no quoting passes it on unchanged there, is left out of the
//     command, which then takes a placeholder, and named escaped beside it
//     (CommandArg, EscapedValue).
//
// bd's own messages keep their lines: a message bd composed, which can carry
// such a value as an error it wraps repeated it, is spelled by PrintedText
// (PrintedTextError, PrintedMessage), which escapes only what a terminal
// acts on and leaves bd's line breaks alone. bd's error printers and its
// doctor check printers spell every message so, as the backstop.
//
// It depends on the standard library only, so every package that prints a
// remedy, internal/storage/dolt included, can use it.
package remedy

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PrintedValue spells value as a refusal names it beside a remedy command: as
// it is when it prints as itself (PrintsAsItself), and otherwise escaped
// (EscapedValue), which spells each character that does not as an escape, so
// the value cannot redraw, reorder or split the command. Unlike
// ui.SanitizeForTerminal, which drops such characters, it still names the
// value bd recorded.
func PrintedValue(value string) string {
	if PrintsAsItself(value) {
		return value
	}
	return EscapedValue(value)
}

// PrintedError is err as a message names it beside a remedy: err itself when
// its text prints as itself, and otherwise an error whose text is spelled by
// PrintedValue and which unwraps to err, so errors.Is and errors.As still
// reach it. It is for a leaf error, one bd did not compose, such as the dial
// or lookup error that repeats a clone-supplied host, a stat error that
// repeats a routed path, or a driver error: such an error is one line, so a
// line break in it came from the value and is escaped too. An error bd
// composed, which can span lines of its own (a refusal with its remedy under
// it), is never wrapped here, since that would print it as one quoted line:
// its values are spelled where they are formatted in, and PrintedText
// neutralizes whatever reaches the terminal.
func PrintedError(err error) error {
	if err == nil || PrintsAsItself(err.Error()) {
		return err
	}
	return printedError{err}
}

type printedError struct{ err error }

func (e printedError) Error() string { return EscapedValue(e.err.Error()) }
func (e printedError) Unwrap() error { return e.err }

// PrintedList spells each of values by PrintedValue and joins them with sep,
// as a refusal names several database or table names.
func PrintedList(values []string, sep string) string {
	printed := make([]string, len(values))
	for i, value := range values {
		printed[i] = PrintedValue(value)
	}
	return strings.Join(printed, sep)
}

// EscapedValue spells value as a refusal names a value its remedy leaves as a
// placeholder, for the user to quote in its place: Go-quoted, as %q spells it,
// with each character ShellQuotedFor refuses on some platform but the
// backslash, which %q already escapes, also spelled as an escape: \x22 for ",
// \x24 for $, \x26 for &, \u201c for “. Between the double quotes is then no
// character a shell ShellQuotedFor names expands, ends the quotes at or splits
// the argument at, so a user who pastes the name as it is printed runs
// nothing, and strconv.Unquote still reads it back as the value.
func EscapedValue(value string) string {
	shellActive := func(r rune) bool {
		return r != '\\' && strings.ContainsRune(unquotableOnPOSIX+unquotableOnWindows, r)
	}
	var escaped strings.Builder
	escaped.WriteByte('"')
	for value != "" {
		// The run up to the next such character is spelled as %q spells it.
		n := strings.IndexFunc(value, shellActive)
		if n < 0 {
			n = len(value)
		}
		quoted := strconv.Quote(value[:n])
		escaped.WriteString(quoted[1 : len(quoted)-1])
		value = value[n:]
		if value == "" {
			break
		}
		r, size := utf8.DecodeRuneInString(value)
		if r < utf8.RuneSelf {
			fmt.Fprintf(&escaped, `\x%02x`, r)
		} else {
			fmt.Fprintf(&escaped, `\u%04x`, r)
		}
		value = value[size:]
	}
	escaped.WriteByte('"')
	return escaped.String()
}

// PrintsAsItself reports whether a terminal shows value as the characters it
// holds: it is valid UTF-8, since a stray byte such as 0x9b starts a control
// sequence in a terminal that reads 8-bit controls, and each rune prints as
// itself (printsAsItself).
func PrintsAsItself(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return !printsAsItself(r) })
}

// printsAsItself reports whether a terminal shows r as itself: r is graphic
// (unicode.IsGraphic), a letter, mark, number, punctuation, symbol or space,
// such as the U+3000 a Japanese IME types, or it is the zero-width non-joiner
// or joiner (U+200C, U+200D), which Persian and Indic text and emoji sequences
// hold and which cannot move the cursor. That rules out a control character,
// such as a carriage return or the escape that starts a terminal sequence,
// any other format character, such as U+202E, which reverses the text after
// it, or U+200B, and a line or paragraph separator, U+2028 or U+2029.
func printsAsItself(r rune) bool {
	return unicode.IsGraphic(r) || r == '\u200c' || r == '\u200d'
}

// PrintedText spells text, a message bd composed that can carry a value (an
// error it prints, a check's detail), so that no character in it acts on the
// terminal but the line breaks and tabs that lay it out: each other rune that
// does not print as itself (PrintsAsItself) is spelled as %q spells it (\r,
// \x1b, \u202e, and \x9b for a stray byte), a carriage return before a line
// break excepted, and everything else is left as it is. Unlike PrintedValue it
// does not quote the text, so a message with no such rune, one line or
// several, prints exactly as before, and spelling one twice changes nothing.
//
// It is the backstop where messages reach the terminal (bd's error printers,
// bd doctor's check output). A line break inside a value is left as it is
// here, so the value itself is spelled by PrintedValue or PrintedError where
// it is formatted in.
func PrintedText(text string) string {
	i := 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		if !keptInText(r, size, text[i+size:]) {
			break
		}
		i += size
	}
	if i == len(text) {
		return text
	}
	var printed strings.Builder
	printed.WriteString(text[:i])
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case keptInText(r, size, text[i+size:]):
			printed.WriteString(text[i : i+size])
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&printed, `\x%02x`, text[i])
		default:
			quoted := strconv.QuoteRune(r)
			printed.WriteString(quoted[1 : len(quoted)-1])
		}
		i += size
	}
	return printed.String()
}

// PrintedMessage is err's text spelled by PrintedText, for a message bd
// composed that is printed to the terminal directly rather than through bd's
// error printers, and "<nil>", as %v spells it, for no error.
func PrintedMessage(err error) string {
	if err == nil {
		return "<nil>"
	}
	return PrintedText(err.Error())
}

// PrintedTextError is err with its text spelled by PrintedText, unwrapping
// to err so errors.Is and errors.As still reach it, for an error bd composed
// (one that can span lines of its own) that it returns to a caller rather
// than prints. It is err itself when PrintedText leaves the text as it is.
func PrintedTextError(err error) error {
	if err == nil || PrintedText(err.Error()) == err.Error() {
		return err
	}
	return printedTextError{err}
}

type printedTextError struct{ err error }

func (e printedTextError) Error() string { return PrintedText(e.err.Error()) }
func (e printedTextError) Unwrap() error { return e.err }

// keptInText reports whether PrintedText leaves the rune r, size bytes long
// and followed by rest, as it is.
func keptInText(r rune, size int, rest string) bool {
	switch {
	case r == utf8.RuneError && size == 1:
		return false
	case r == '\n', r == '\t', printsAsItself(r):
		return true
	}
	return r == '\r' && strings.HasPrefix(rest, "\n")
}

// TextWriter returns a writer that spells what is written to it by
// PrintedText before passing it to w, for a printer that writes whole
// messages to w, such as cobra's "Error: ..." line. A write that splits a
// UTF-8 sequence spells each part as stray bytes.
func TextWriter(w io.Writer) io.Writer { return textWriter{w} }

type textWriter struct{ w io.Writer }

func (t textWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(t.w, PrintedText(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// ShellQuotedFor quotes value as one argument of a command printed for a user
// to paste into a shell on goos, and reports whether it can: a remedy prints a
// value in a command only when the shells people run there all pass it to bd
// unchanged, and otherwise names it beside a placeholder.
//
// On every platform it refuses a value that does not print as itself
// (PrintsAsItself): a line break ends the command in each shell, a carriage
// return or terminal escape sequence can redraw the printed command so the
// one a user copies is not the one bd printed, and a character such as U+202E
// or U+2028 can reorder or split how the command shows. It also refuses a
// value that starts with -, which no quoting keeps the program the command
// runs (nc, chmod, git, bd) from reading as an option, such as a dolt.host of
// -e/bin/sh.
//
// On POSIX a value goes in single quotes, which sh, bash, zsh, fish and
// PowerShell keep literal, unless it holds:
//   - \, which fish reads as an escape there, so a backslash before a quote
//     ends them;
//   - ', which ends them in each: spelled as \' between two quoted parts, sh,
//     bash, zsh and fish read it as a quote, but PowerShell, which takes a
//     backslash literally, does not, and runs the rest of the value as a
//     command;
//   - ‘ ’ ‚ ‛ (U+2018 to U+201B), which end them in PowerShell.
//
// On Windows a value goes in plain double quotes, inside which cmd.exe,
// PowerShell and Git Bash keep spaces, backslashes and ( ) ' # @ literal, as
// Go's argument parsing does, unless it holds:
//   - ", which ends the quotes in each, or “ ” „, which end them in PowerShell;
//   - $ or `, which PowerShell and bash expand there;
//   - % or !, which cmd.exe expands there (! with delayed expansion on), and
//     bash expands ! as history;
//   - & | < > or ^, which cmd.exe acts on when PowerShell hands a .cmd shim,
//     such as npm's bd.cmd, an argument with no space in it unquoted, or ; ,
//     or =, which cmd.exe reads there as separating a batch file's
//     arguments, so a shim that reads them one by one (%1, %2) rather than
//     passing them all on (%*), as npm's does, hands bd a host such as
//     db.example.com;--recreate-missing as two (each refused whether or not
//     the value has a space, so a path such as R&D planning gets a
//     placeholder too);
//   - \\, which bash reads there as one backslash;
//
// or ends in a backslash, which Go's argument parsing reads as escaping the
// closing quote. cmd.exe needs it doubled, and PowerShell, which takes
// backslashes literally, would then pass both on.
func ShellQuotedFor(goos, value string) (string, bool) {
	if !PrintsAsItself(value) || strings.HasPrefix(value, "-") {
		return "", false
	}
	if goos != "windows" {
		if strings.ContainsAny(value, unquotableOnPOSIX) {
			return "", false
		}
		return "'" + value + "'", true
	}
	if strings.HasSuffix(value, `\`) || strings.Contains(value, `\\`) || strings.ContainsAny(value, unquotableOnWindows) {
		return "", false
	}
	return `"` + value + `"`, true
}

// ShellWord spells value as one argument of a command printed for a user to
// paste into a shell on goos, and reports whether it can: bare when it is a
// plain word (IsPlainWord), which every shell on every platform passes on as
// it is, such as an issue ID, a host name or a path of plain characters, and
// otherwise as ShellQuotedFor quotes it, or refused where that refuses it.
func ShellWord(goos, value string) (string, bool) {
	if IsPlainWord(value) {
		return value, true
	}
	return ShellQuotedFor(goos, value)
}

// CommandArg spells value as one argument of a command printed for a user to
// paste into a shell on goos, the way every remedy that pastes a value into a
// command does. When ShellWord passes value on, CommandArg returns it so
// spelled and an empty note. Otherwise the command takes placeholder in its
// place, and note, to print after the command, names value escaped
// (EscapedValue) for the user to quote for their shell:
//
//	chmod 700 <path>  (<path>: "/home/x\r\x1b[2K")
func CommandArg(goos, value, placeholder string) (arg, note string) {
	if word, ok := ShellWord(goos, value); ok {
		return word, ""
	}
	return placeholder, "  (" + placeholder + ": " + EscapedValue(value) + ")"
}

// PrintedWord spells value as a hint names it on the lines printed around a
// command it pastes value into, which a user can paste with the command: as
// it is when it is a plain word (IsPlainWord), as an issue ID bd mints is,
// and otherwise escaped (EscapedValue), so pasting those lines runs no part of
// it and it cannot redraw the command. PrintedValue alone would leave a $(...)
// as it is.
func PrintedWord(value string) string {
	if IsPlainWord(value) {
		return value
	}
	return EscapedValue(value)
}

// IsPlainWord reports whether value is non-empty and holds only ASCII
// letters, digits and _ . / : + -, characters no shell bd prints a command
// for (sh, bash, zsh, fish, PowerShell, cmd.exe, a .cmd shim) expands, splits
// or quotes at, and does not start with -, which the program a command runs
// would read as an option.
func IsPlainWord(value string) bool {
	if value == "" || value[0] == '-' {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("_./:+-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// unquotableOnPOSIX and unquotableOnWindows hold the characters a shell there
// does not keep literal inside the quotes ShellQuotedFor puts a value in, or,
// on Windows, in the argument PowerShell hands a .cmd shim unquoted.
// EscapedValue spells each of them as an escape.
const (
	unquotableOnPOSIX   = "\\'‘’‚‛"
	unquotableOnWindows = "\"“”„$`%!&|<>^;,="
)
