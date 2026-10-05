package doltserver

import (
	"bytes"
	"strings"
	"testing"
)

// writeChunks feeds s to a new watcher for port in the given chunk sizes
// (the last size repeats) and returns the watcher and what reached dst.
func writeChunks(t *testing.T, port int, s string, sizes ...int) (*StartupWatch, string) {
	t.Helper()
	var dst bytes.Buffer
	w := NewStartupWatch(&dst, port)
	b := []byte(s)
	for i := 0; len(b) > 0; i++ {
		n := sizes[len(sizes)-1]
		if i < len(sizes) {
			n = sizes[i]
		}
		if n > len(b) {
			n = len(b)
		}
		got, err := w.Write(b[:n])
		if err != nil || got != n {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", got, err, n)
		}
		b = b[n:]
	}
	return w, dst.String()
}

func TestStartupWatch(t *testing.T) {
	ready := `time="2026-10-04T05:45:58Z" level=info msg="Server ready. Accepting connections."` + "\n"
	pad := strings.Repeat("x", 5000) + "\n"
	for _, tc := range []struct {
		name      string
		port      int
		out       string
		sizes     []int
		ready     bool
		portInUse bool
	}{
		{name: "ready line in one write", port: 3306, out: ready, sizes: []int{1 << 20}, ready: true},
		{name: "ready line split across writes", port: 3306, out: ready, sizes: []int{40, 30, 1 << 20}, ready: true},
		{name: "ready line one byte per write", port: 3306, out: ready, sizes: []int{1}, ready: true},
		{name: "ready line with CRLF", port: 3306, out: strings.ReplaceAll(ready, "\n", "\r\n"), sizes: []int{7}, ready: true},
		{name: "ready line after more than the tail of output", port: 3306, out: pad + ready, sizes: []int{1000}, ready: true},
		{name: "MCP ready line is not the SQL ready line", port: 3306, out: "Dolt MCP server ready. Accepting connections.\n", sizes: []int{1 << 20}},
		{name: "dolt pre-check", port: 8000, out: "Port 8000 already in use.\n", sizes: []int{5}, portInUse: true},
		{name: "dolt pre-check at log_level warning", port: 39847, out: "Starting server with Config HP=\"127.0.0.1:39847\"|T=\"28800000\"|R=\"false\"|L=\"warning\"\nPort 39847 already in use.\n", sizes: []int{1 << 20}, portInUse: true},
		{name: "dolt pre-check for another port", port: 80, out: "Port 8000 already in use.\n", sizes: []int{1 << 20}},
		{name: "go-mysql-server pre-check", port: 41234, out: "Port 127.0.0.1:41234 already in use.\n", sizes: []int{3}, portInUse: true},
		{name: "kernel bind failure", port: 41234, out: "listen tcp 127.0.0.1:41234: bind: address already in use\n", sizes: []int{1}, portInUse: true},
		{name: "windows bind failure", port: 41234, out: "listen tcp 127.0.0.1:41234: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.\r\n", sizes: []int{9}, portInUse: true},
		{name: "another listener's bind failure", port: 41234, out: "listen tcp :9091: bind: address already in use\n", sizes: []int{1 << 20}},
		{name: "unix socket warning", port: 41234, out: "unix socket set up failed: bind address at given unix socket path is already in use\n", sizes: []int{1 << 20}},
		{name: "port-in-use after the tail of output", port: 41234, out: pad + "Port 41234 already in use.\n", sizes: []int{999}, portInUse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, got := writeChunks(t, tc.port, tc.out, tc.sizes...)
			if got != tc.out {
				t.Errorf("every byte must reach the log unchanged: got %q", got)
			}
			if w.IsReady() != tc.ready {
				t.Errorf("IsReady = %v, want %v", w.IsReady(), tc.ready)
			}
			if w.SawPortInUse() != tc.portInUse {
				t.Errorf("SawPortInUse = %v, want %v", w.SawPortInUse(), tc.portInUse)
			}
		})
	}
}

func TestStartupWatchNilLog(t *testing.T) {
	w := NewStartupWatch(nil, 3306)
	n, err := w.Write([]byte("Server ready. Accepting connections.\n"))
	if err != nil || n != 37 {
		t.Fatalf("Write = (%d, %v), want (37, nil)", n, err)
	}
	if !w.IsReady() {
		t.Fatal("ready line not seen")
	}
}
