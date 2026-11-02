package main

// This file gives the bd test binary a second life as a stand-in `dolt`.
// The version-upgrade flagship test (version_upgrade_server_restart_test.go)
// installs a copy of this binary under that name on PATH, so a
// doltserver.Start under test launches and probes a `dolt` whose process
// command line names `dolt` — the shape doltserver's ps-based process checks
// match — without a real dolt. TestMain dispatches to fakeDoltDispatch
// before any test machinery runs. The env contract mirrors the
// internal/doltserver test fake (fakedolt_test.go), which documents it; the
// two agree on the variable names so a future shared harness stays possible.

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	testFakeDoltEnv      = "BEADS_TEST_FAKE_DOLT"
	testFakeDoltDelayEnv = "BEADS_TEST_FAKE_DOLT_DELAY"
	// testFakeDoltInUseEnv lists ports ("all" for every port) the fake
	// reports taken without trying to bind, as if another process grabbed
	// them.
	testFakeDoltInUseEnv = "BEADS_TEST_FAKE_DOLT_INUSE_PORTS"
	// testFakeDoltExitEnv makes the fake exit with that status after its
	// startup delay instead of binding.
	testFakeDoltExitEnv = "BEADS_TEST_FAKE_DOLT_EXIT"
	// testFakeDoltLaunchesEnv names a file the fake appends each
	// sql-server launch's port to.
	testFakeDoltLaunchesEnv = "BEADS_TEST_FAKE_DOLT_LAUNCHES"
	// testFakeDoltVersionEnv is the version `dolt version` reports, 2.6.0
	// when unset.
	testFakeDoltVersionEnv = "BEADS_TEST_FAKE_DOLT_VERSION"
)

var fakeDoltConfigPortRe = regexp.MustCompile(`(?m)^\s+port:\s*(\d+)`)

// fakeDoltDispatch answers the dolt invocations doltserver.Start makes when
// this binary is executed as the installed `dolt`, and reports whether it
// did (so TestMain can exit with the fake's status before any test runs).
func fakeDoltDispatch() (int, bool) {
	if os.Getenv(testFakeDoltEnv) == "" || len(os.Args) < 2 {
		return 0, false
	}
	switch os.Args[1] {
	case "version", "config", "init", "sql-server":
		return fakeDoltMain(os.Args[1:]), true
	}
	return 0, false
}

// fakeDoltMain behaves like the internal/doltserver test fake: its
// sql-server spends the configured delay on "startup", then binds its port
// (dolt's "Port N already in use." and exit 1 when it is taken), greets
// every connection with a few stand-in handshake bytes until killed, and
// never logs the ready line, like dolt at log_level warning.
func fakeDoltMain(args []string) int {
	switch args[0] {
	case "version":
		version := os.Getenv(testFakeDoltVersionEnv)
		if version == "" {
			version = "2.6.0"
		}
		fmt.Println("dolt version " + version)
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
			b, err := os.ReadFile(args[i+1]) //nolint:gosec // G304: the config path doltserver generated
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
	if path := os.Getenv(testFakeDoltLaunchesEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // G304: test-controlled path
			_, _ = fmt.Fprintf(f, "%d\n", port)
			_ = f.Close()
		}
	}
	if d, err := time.ParseDuration(os.Getenv(testFakeDoltDelayEnv)); err == nil {
		time.Sleep(d)
	}
	if code, err := strconv.Atoi(os.Getenv(testFakeDoltExitEnv)); err == nil {
		fmt.Println("fake dolt: failing startup on purpose")
		return code
	}
	for _, p := range strings.Split(os.Getenv(testFakeDoltInUseEnv), ",") {
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
	for {
		c, err := ln.Accept()
		if err != nil {
			return 1
		}
		_, _ = c.Write([]byte("\x0a5.7.9-fake-dolt\x00"))
		_ = c.Close()
	}
}