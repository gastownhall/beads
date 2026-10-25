package doltserver_test

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// fakeDoltEnv, when set, makes this test binary act as a stand-in `dolt`
// (see fakeDolt). TestMain checks it before anything else. Tests reach it
// through a `dolt` shim on PATH that execs this binary; see
// installFakeDolt in startowned_test.go, which repeats these names.
const (
	fakeDoltEnv      = "BEADS_TEST_FAKE_DOLT"
	fakeDoltDelayEnv = "BEADS_TEST_FAKE_DOLT_DELAY"
	// fakeDoltInUseEnv lists ports ("all" for every port) the fake reports
	// taken without trying to bind, as if another process grabbed them.
	fakeDoltInUseEnv = "BEADS_TEST_FAKE_DOLT_INUSE_PORTS"
	// fakeDoltExitEnv makes the fake exit with that status after its
	// startup delay instead of binding.
	fakeDoltExitEnv = "BEADS_TEST_FAKE_DOLT_EXIT"
	// fakeDoltReadyEnv makes the fake log dolt's ready line once bound, as
	// dolt does at log level info or debug.
	fakeDoltReadyEnv = "BEADS_TEST_FAKE_DOLT_READY"
	// fakeDoltLaunchesEnv names a file the fake appends each sql-server
	// launch's port to.
	fakeDoltLaunchesEnv = "BEADS_TEST_FAKE_DOLT_LAUNCHES"
)

var fakeDoltConfigPortRe = regexp.MustCompile(`(?m)^\s+port:\s*(\d+)`)

// fakeDolt answers the dolt invocations doltserver.Start makes. Its
// sql-server behaves like dolt's where port races are concerned: it spends
// $BEADS_TEST_FAKE_DOLT_DELAY on "startup", then binds its port; if the port
// is taken it prints dolt's "Port N already in use." and exits 1, otherwise it
// greets every connection with a few bytes (a stand-in MySQL handshake) until
// killed. It never logs the ready line, like dolt at log_level warning.
func fakeDolt(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println("dolt version 2.1.8")
		return 0
	case "config":
		fmt.Println("fake")
		return 0
	case "init":
		if err := os.MkdirAll(".dolt", 0o750); err != nil {
			return 1
		}
		return 0
	case "sql-server":
	default:
		return 2
	}
	host, port := "127.0.0.1", 0
	for i := 1; i+1 < len(args); i++ {
		switch args[i] {
		case "--config":
			b, err := os.ReadFile(args[i+1])
			if err != nil {
				fmt.Println(err)
				return 1
			}
			if m := fakeDoltConfigPortRe.FindSubmatch(b); m != nil {
				port, _ = strconv.Atoi(string(m[1]))
			}
		case "-P":
			port, _ = strconv.Atoi(args[i+1])
		case "-H":
			host = args[i+1]
		}
	}
	if port == 0 {
		fmt.Println("fake dolt: no port")
		return 2
	}
	if path := os.Getenv(fakeDoltLaunchesEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // G304: test-controlled path
			_, _ = fmt.Fprintf(f, "%d\n", port)
			_ = f.Close()
		}
	}
	if d, err := time.ParseDuration(os.Getenv(fakeDoltDelayEnv)); err == nil {
		time.Sleep(d)
	}
	if code, err := strconv.Atoi(os.Getenv(fakeDoltExitEnv)); err == nil {
		fmt.Println("fake dolt: failing startup on purpose")
		return code
	}
	for _, p := range strings.Split(os.Getenv(fakeDoltInUseEnv), ",") {
		if p == "all" || p == strconv.Itoa(port) {
			fmt.Printf("Port %d already in use.\n", port)
			return 1
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		fmt.Printf("Port %d already in use.\n", port)
		return 1
	}
	if os.Getenv(fakeDoltReadyEnv) != "" {
		fmt.Println(`time="2026-10-04T12:00:00Z" level=info msg="Server ready. Accepting connections."`)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return 1
		}
		_, _ = c.Write([]byte("\x0a5.7.9-fake-dolt\x00"))
		_ = c.Close()
	}
}
