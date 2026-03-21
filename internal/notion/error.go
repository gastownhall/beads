package notion

import "fmt"

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
