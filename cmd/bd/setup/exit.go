package setup

import (
	"errors"
	"fmt"
	"os"
)

var ErrSilent = errors.New("setup: failure already reported")

func HandleError(format string, args ...interface{}) error {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	return ErrSilent
}

func HandleErrorWithHint(message, hint string) error {
	fmt.Fprintf(os.Stderr, "Error: %s\n", message)
	fmt.Fprintf(os.Stderr, "Hint: %s\n", hint)
	return ErrSilent
}

func SilentExit() error {
	return ErrSilent
}
