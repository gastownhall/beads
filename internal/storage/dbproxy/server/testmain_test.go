package server_test

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"testing"
)

// TestMain lets this test binary stand in for `dolt` (see fakeDolt).
//
// Hotfix-line note: main's TestMain also claims a suite temp root and sweeps
// leaked servers (#6441). That suite-lifecycle machinery is not on this
// release line, so only the fake-dolt hook from #7184 is carried here.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeDoltEnv); mode != "" {
		os.Exit(fakeDolt(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeDoltEnv, when set, makes this test binary act as a stand-in `dolt`
// (see fakeDolt) so tests can drive DoltServer through startup behaviors a
// real dolt cannot be made to show on demand. The internal tests set it and
// pass os.Args[0] as the dolt binary.
const fakeDoltEnv = "BEADS_TEST_FAKE_DOLT"

var fakeDoltPortRe = regexp.MustCompile(`(?m)^\s+port:\s*(\d+)`)

// fakeDolt answers the dolt invocations DoltServer.Start makes. For
// `sql-server --config <file>` it reads listener.port from the file and, by
// mode:
//   - "inuse": reports dolt's own port-in-use error and exits 1;
//   - "silent": listens but never logs the ready line;
//   - "ready": listens and logs the ready line.
func fakeDolt(mode string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "config":
		fmt.Println("fake")
		return 0
	case "init":
		return 0
	case "sql-server":
	default:
		return 2
	}
	var cfgPath string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--config" {
			cfgPath = args[i+1]
		}
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	m := fakeDoltPortRe.FindSubmatch(body)
	if m == nil {
		fmt.Fprintln(os.Stderr, "no listener.port in", cfgPath)
		return 1
	}
	port := string(m[1])
	if mode == "inuse" {
		fmt.Fprintf(os.Stderr, "Port %s already in use.\n", port)
		return 1
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mode == "ready" {
		fmt.Fprintln(os.Stderr, `level=info msg="Server ready. Accepting connections."`)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return 0
		}
		defer c.Close()
	}
}
