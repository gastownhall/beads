package debug

import (
	"fmt"
	"os"
)

var enabled = os.Getenv("BD_DEBUG") != ""

func Enabled() bool {
	return enabled
}

func Logf(format string, args ...interface{}) {
	if enabled {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func Printf(format string, args ...interface{}) {
	if enabled {
		fmt.Printf(format, args...)
	}
}
