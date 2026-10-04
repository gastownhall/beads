package doltserver_test

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"time"
)

// fakeDoltEnv, when set, makes this test binary act as a stand-in `dolt`
// (see fakeDolt). TestMain checks it before anything else. Tests reach it
// through a `dolt` shim on PATH that execs this binary; see
// installFakeDolt in startowned_test.go, which repeats these names.
const (
	fakeDoltEnv      = "BEADS_TEST_FAKE_DOLT"
	fakeDoltDelayEnv = "BEADS_TEST_FAKE_DOLT_DELAY"
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
	if d, err := time.ParseDuration(os.Getenv(fakeDoltDelayEnv)); err == nil {
		time.Sleep(d)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		fmt.Printf("Port %d already in use.\n", port)
		return 1
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
