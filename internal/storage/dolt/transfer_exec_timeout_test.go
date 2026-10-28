package dolt

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// TestTransferExecTimeoutDuration verifies BEADS_DOLT_TRANSFER_TIMEOUT parsing:
// valid durations and bare seconds are honored, unset/invalid/non-positive
// values fall back to the compiled-in default. This is the server-mode sibling
// of TestCLIExecTimeoutDuration; before it existed the deadline on the one-shot
// CALL DOLT_PUSH / DOLT_PULL connection was a hardcoded 5m with no override, so
// a store large enough to need longer could not be pushed at all.
func TestTransferExecTimeoutDuration(t *testing.T) {
	t.Run("duration string", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "90m")
		if got := transferExecTimeoutDuration(); got != 90*time.Minute {
			t.Fatalf("transferExecTimeoutDuration() = %v, want 90m", got)
		}
	})
	t.Run("bare seconds", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "5400")
		if got := transferExecTimeoutDuration(); got != 5400*time.Second {
			t.Fatalf("transferExecTimeoutDuration() = %v, want 5400s", got)
		}
	})
	t.Run("unset falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "eventually")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
	t.Run("non-positive falls back", func(t *testing.T) {
		t.Setenv(transferExecTimeoutEnv, "-1m")
		if got := transferExecTimeoutDuration(); got != transferExecTimeout {
			t.Fatalf("transferExecTimeoutDuration() = %v, want default %v", got, transferExecTimeout)
		}
	})
}

// transferScript scripts how a stalling fake MySQL server answers each
// statement: map a statement prefix (e.g. "CALL DOLT_PULL") to one of:
//
//	"stall"  hold the connection open without answering (the default)
//	"drop"   close the connection without answering
//	"ok"     answer with an OK packet
//	"err:<message>"  answer with a MySQL error packet, code 1105
//
// Statements that start a transaction, set a variable, or end a transaction
// always answer OK — the scripted behavior is for the transfer CALLs.
type transferScript map[string]string

// startStallingMySQL starts a fake MySQL server on 127.0.0.1 that completes
// the handshake and then answers each COM_QUERY per script, and returns the
// port it listens on. Modeled on serveMinimalMySQL
// (internal/storage/uow/upstream_outage_window_test.go): greeting, OK to any
// auth response, then scripted query answers. The server never sets its own
// deadline, so a stall holds until the CLIENT closes the connection — which is
// the client's read deadline firing, the behavior under test.
func startStallingMySQL(t *testing.T, script transferScript) (port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				serveStallingMySQL(conn, script)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// serveStallingMySQL speaks just enough of the server side of the MySQL
// protocol for go-sql-driver to connect and run statements against script.
// The DSNs under test pin maxAllowedPacket and interpolate parameters
// client-side (internal/storage/doltutil/dsn.go), so no connect-time probe or
// prepared-statement round-trips occur: after the handshake every statement
// arrives as one COM_QUERY.
func serveStallingMySQL(conn net.Conn, script transferScript) {
	defer conn.Close()
	const (
		clientLongPassword = 1 << 0
		clientProtocol41   = 1 << 9
		clientTransactions = 1 << 13
		clientSecureConn   = 1 << 15
		clientPluginAuth   = 1 << 19
	)
	caps := uint32(clientLongPassword | clientProtocol41 | clientTransactions | clientSecureConn | clientPluginAuth)
	g := []byte{0x0a}
	g = append(g, "8.0.0-fake\x00"...)
	g = binary.LittleEndian.AppendUint32(g, 1)
	g = append(g, "abcdefgh"...) // auth data part 1
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps))
	g = append(g, 0x21) // utf8_general_ci
	g = binary.LittleEndian.AppendUint16(g, 0x0002)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
	g = append(g, 21)
	g = append(g, make([]byte, 10)...)
	g = append(g, "ijklmnopqrst\x00"...) // auth data part 2
	g = append(g, "mysql_native_password\x00"...)
	ok := []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}

	write := func(seq byte, payload []byte) error {
		hdr := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
		_, err := conn.Write(append(hdr, payload...))
		return err
	}
	read := func() (seq byte, body []byte, err error) {
		var hdr [4]byte
		if _, err = io.ReadFull(conn, hdr[:]); err != nil {
			return 0, nil, err
		}
		body = make([]byte, int(hdr[0])|int(hdr[1])<<8|int(hdr[2])<<16)
		_, err = io.ReadFull(conn, body)
		return hdr[3], body, err
	}

	if write(0, g) != nil {
		return
	}
	seq, _, err := read() // handshake response
	if err != nil || write(seq+1, ok) != nil {
		return
	}
	for {
		seq, body, err := read()
		if err != nil || len(body) == 0 || body[0] == 0x01 { // EOF or COM_QUIT
			return
		}
		if body[0] != 0x03 { // not COM_QUERY
			if write(seq+1, ok) != nil {
				return
			}
			continue
		}
		switch transferScriptAction(script, string(body[1:])) {
		case "drop":
			return // close without answering
		case "stall":
			// Answer nothing: the read at the top of the loop blocks until the
			// client's read deadline fires and it closes the connection.
		case "ok":
			if write(seq+1, ok) != nil {
				return
			}
		default: // "err:<message>"
			msg := strings.TrimPrefix(transferScriptAction(script, string(body[1:])), "err:")
			e := []byte{0xff, 0x51, 0x04, '#'} // 1105 = 0x0451
			e = append(e, "HY000"...)
			e = append(e, msg...)
			if write(seq+1, e) != nil {
				return
			}
		}
	}
}

// transferScriptAction resolves how the fake server answers query: the
// longest script prefix that matches, or OK for transaction/SET housekeeping
// statements, or "stall" for anything unrecognized.
func transferScriptAction(script transferScript, query string) string {
	for _, prefix := range []string{"START TRANSACTION", "SET", "COMMIT", "ROLLBACK", "BEGIN"} {
		if strings.HasPrefix(query, prefix) {
			return "ok"
		}
	}
	best, action := "", ""
	for prefix, act := range script {
		if strings.HasPrefix(query, prefix) && len(prefix) > len(best) {
			best, action = prefix, act
		}
	}
	if best != "" {
		return action
	}
	return "stall"
}

// newTransferStore returns a DoltStore whose connStr points at a fake stalling
// MySQL server answering per script. The transfer sites under test read only
// connStr and branch; every caller that needs more (s.db and friends) is not
// under test here.
func newTransferStore(t *testing.T, script transferScript) *DoltStore {
	t.Helper()
	cfg := &Config{ServerHost: "127.0.0.1", ServerPort: startStallingMySQL(t, script), ServerUser: "root"}
	return &DoltStore{connStr: buildServerDSN(cfg, "beads"), branch: "main"}
}

// TestTransferPushStallHitsConfiguredDeadline pins GH#6375's push half: with
// BEADS_DOLT_TRANSFER_TIMEOUT=1s, a server that completes the handshake but
// never answers CALL DOLT_PUSH must fail in about a second — not hang until
// the 5m hardcoded deadline (or a test guard) — and the error must still wrap
// mysql.ErrInvalidConn while naming the deadline that fired and the variable
// that raises it.
func TestTransferPushStallHitsConfiguredDeadline(t *testing.T) {
	const env = "BEADS_DOLT_TRANSFER_TIMEOUT"
	t.Setenv(env, "1s")

	s := newTransferStore(t, transferScript{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("push to a stalling server unexpectedly succeeded")
	}
	if elapsed > 5*time.Second {
		t.Errorf("push ran %v with %s=1s; the read deadline ignores the variable (err: %v)", elapsed.Round(time.Second), env, err)
	}
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Errorf("push error = %v, want it still wrapping mysql.ErrInvalidConn", err)
	}
	if msg := err.Error(); !strings.Contains(msg, env) || !strings.Contains(msg, "1s") {
		t.Errorf("push error does not name the deadline or %s: %q", env, msg)
	}
}

// TestTransferReadTimeoutConfigurable reproduces GH#6375 end to end: the read
// deadline on the one-shot DOLT_PUSH connection was hardcoded to 5m, so a
// server that never even completes the handshake left the push hanging until
// an outer bound fired. Setting BEADS_DOLT_TRANSFER_TIMEOUT must bound the
// transfer, and the resulting error must name the variable that raises it.
func TestTransferReadTimeoutConfigurable(t *testing.T) {
	const env = "BEADS_DOLT_TRANSFER_TIMEOUT"
	t.Setenv(env, "1s")

	// A server that accepts and never answers: the client's first read (the
	// MySQL handshake) blocks until the connection's read deadline fires.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	s := &DoltStore{connStr: "root@tcp(" + ln.Addr().String() + ")/beads"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	err = s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error from a server that never answers")
	}
	if elapsed > 5*time.Second {
		t.Errorf("transfer ran %v with %s=1s; the read deadline ignores the variable (err: %v)", elapsed.Round(time.Second), env, err)
	}
	if !strings.Contains(err.Error(), env) {
		t.Errorf("transfer error does not name %s: %v", env, err)
	}
}