package output

import (
	"encoding/json"
	"fmt"
	"io"
)

type IO struct {
	stdout io.Writer
	stderr io.Writer
	json   bool
}

func NewIO(stdout, stderr io.Writer) *IO {
	return &IO{stdout: stdout, stderr: stderr}
}

func (ioo *IO) WithJSON(enabled bool) *IO {
	return &IO{
		stdout: ioo.stdout,
		stderr: ioo.stderr,
		json:   enabled,
	}
}

func (ioo *IO) JSONEnabled() bool {
	return ioo.json
}

func (ioo *IO) JSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(ioo.stdout, string(data))
	return err
}

func (ioo *IO) Text(format string, args ...any) error {
	_, err := fmt.Fprintf(ioo.stdout, format+"\n", args...)
	return err
}

func (ioo *IO) Progress(format string, args ...any) error {
	_, err := fmt.Fprintf(ioo.stderr, format+"\n", args...)
	return err
}

func (ioo *IO) PrintError(err error) {
	if ioo.json {
		payload := map[string]any{
			"error": err.Error(),
		}
		if cliErr, ok := err.(*Error); ok {
			payload["error"] = cliErr.What
			payload["why"] = cliErr.Why
			if cliErr.Hint != "" {
				payload["hint"] = cliErr.Hint
			}
		}
		_ = ioo.JSON(payload)
		return
	}

	if cliErr, ok := err.(*Error); ok {
		_, _ = fmt.Fprintf(ioo.stderr, "Error: %s\n", cliErr.What)
		if cliErr.Why != "" {
			_, _ = fmt.Fprintf(ioo.stderr, "  Why: %s\n", cliErr.Why)
		}
		if cliErr.Hint != "" {
			_, _ = fmt.Fprintf(ioo.stderr, "  Hint: %s\n", cliErr.Hint)
		}
		return
	}

	_, _ = fmt.Fprintf(ioo.stderr, "Error: %v\n", err)
}

type Error struct {
	What string
	Why  string
	Hint string
	Code int
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.What, e.Err)
	}
	if e.What != "" {
		return e.What
	}
	return "command failed"
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (e *Error) ExitCode() int {
	if e.Code <= 0 {
		return 1
	}
	return e.Code
}

func NewError(what, why, hint string, code int) *Error {
	return &Error{What: what, Why: why, Hint: hint, Code: code}
}

func Wrap(err error, what string) *Error {
	return &Error{What: what, Err: err, Code: 1}
}
