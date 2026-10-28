//go:build cgo

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRuntimeCommandHonorsCentralServerConfig reproduces GH#3895: a
// server-mode workspace whose metadata.json names no host or port must
// connect to the endpoint ~/.config/beads/server.json names. bd list should
// dial the central host:port, not 127.0.0.1.
func TestRuntimeCommandHonorsCentralServerConfig(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	namedPortWorkspace(t, f, 0)
	central := filepath.Join(f.home, "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user"}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)

	res := f.run("", "list")

	// It launches no dolt server: the central endpoint is not one bd owns.
	requireNoLaunch(t, res)
	want := net.JoinHostPort("db.invalid", strconv.Itoa(port))
	if !strings.Contains(res.output, want) {
		t.Errorf("bd list did not use the central server config endpoint %q:\n%s", want, res.output)
	}
}
