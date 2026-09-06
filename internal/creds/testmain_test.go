package creds

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/credentialcmd"
)

func TestMain(m *testing.M) {
	if code, ok := credentialcmd.Dispatch(); ok {
		os.Exit(code)
	}
	code := m.Run()
	if err := credentialcmd.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "credential command fixture cleanup: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
