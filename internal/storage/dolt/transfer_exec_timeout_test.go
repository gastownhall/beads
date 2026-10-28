package dolt

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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

// TestTransferPushStallHitsConfiguredDeadline pins GH#6375 on the SQL push
// route: a CALL DOLT_PUSH the server never answers must die at the configured
// BEADS_DOLT_TRANSFER_TIMEOUT, not at a hardcoded 5m, and the error must name
// the deadline that fired and the variable that raises it while still being
// the driver's ErrInvalidConn.
func TestTransferPushStallHitsConfiguredDeadline(t *testing.T) {
	t.Setenv("BEADS_DOLT_TRANSFER_TIMEOUT", "1s")
	s := stallingStore(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
	assertTransferDeadlineHit(t, err, time.Since(start))
}

// TestTransferSitesStallHitConfiguredDeadline extends the push pin to the
// other server-side transfers: the SQL pull (and both statements of its
// GH#3144 fetch+merge fallback) and federation's push and fetch each die at
// the configured deadline with the annotated ErrInvalidConn.
func TestTransferSitesStallHitConfiguredDeadline(t *testing.T) {
	t.Setenv("BEADS_DOLT_TRANSFER_TIMEOUT", "1s")
	noTracking := errStep(1105, "You must specify a branch to pull from: did not specify a branch")
	pull := func(ctx context.Context, s *DoltStore) error {
		_, err := s.pullWithAutoResolveUnchecked(ctx, "origin", "CALL DOLT_PULL(?, ?)", "origin", "main")
		return err
	}
	for _, tc := range []struct {
		name   string
		script map[string]fakeStep
		run    func(context.Context, *DoltStore) error
	}{
		{"pull", nil, pull},
		{"pull fallback fetch", map[string]fakeStep{"CALL DOLT_PULL": noTracking}, pull},
		{"pull fallback merge", map[string]fakeStep{"CALL DOLT_PULL": noTracking, "CALL DOLT_FETCH": okStep}, pull},
		{"federation push", nil, func(ctx context.Context, s *DoltStore) error {
			return s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", "peer", "main")
		}},
		{"federation fetch", nil, func(ctx context.Context, s *DoltStore) error {
			return s.execWithLongTimeout(ctx, "CALL DOLT_FETCH(?)", "peer")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := stallingStore(t, tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			start := time.Now()
			err := tc.run(ctx, s)
			assertTransferDeadlineHit(t, err, time.Since(start))
		})
	}
}

// assertTransferDeadlineHit checks a transfer stalled against a 1s
// BEADS_DOLT_TRANSFER_TIMEOUT: it failed between 1s and 5s with the driver's
// lost-connection error, annotated with the deadline and the variable.
func assertTransferDeadlineHit(t *testing.T, err error, elapsed time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error from a server that never answers the CALL")
	}
	if elapsed < time.Second || elapsed > 5*time.Second {
		t.Errorf("transfer failed after %v, want 1s-5s with BEADS_DOLT_TRANSFER_TIMEOUT=1s (err: %v)", elapsed.Round(time.Millisecond), err)
	}
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Errorf("error does not wrap mysql.ErrInvalidConn: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "1s read deadline") {
		t.Errorf("error does not name the 1s deadline: %v", err)
	}
	if !strings.Contains(msg, "BEADS_DOLT_TRANSFER_TIMEOUT") {
		t.Errorf("error does not name BEADS_DOLT_TRANSFER_TIMEOUT: %v", err)
	}
}

// TestAnnotateTransferTimeout pins that a lost-connection failure on a
// transfer connection names the deadline and the env var that raises it only
// when the statement ran at least as long as that deadline. An earlier loss
// (server restart, proxy reap) or any other error class is not a deadline
// expiry and comes back untouched.
func TestAnnotateTransferTimeout(t *testing.T) {
	const deadline = 5 * time.Minute

	t.Run("nil stays nil", func(t *testing.T) {
		if err := annotateTransferTimeout(nil, deadline, deadline); err != nil {
			t.Fatalf("annotateTransferTimeout(nil) = %v, want nil", err)
		}
	})

	lostConn := []struct {
		name string
		err  error
	}{
		{"mysql invalid conn", mysql.ErrInvalidConn},
		{"driver bad conn", driver.ErrBadConn},
		{"os deadline exceeded", os.ErrDeadlineExceeded},
		{"wrapped", fmt.Errorf("failed to push to origin/main: %w", mysql.ErrInvalidConn)},
	}
	for _, tc := range lostConn {
		t.Run(tc.name+" at the deadline is annotated", func(t *testing.T) {
			got := annotateTransferTimeout(tc.err, deadline, deadline)
			msg := got.Error()
			if !strings.Contains(msg, "5m0s") {
				t.Errorf("message does not name the deadline that fired: %q", msg)
			}
			if !strings.Contains(msg, transferExecTimeoutEnv) {
				t.Errorf("message does not name %s: %q", transferExecTimeoutEnv, msg)
			}
			if !strings.Contains(msg, tc.err.Error()) {
				t.Errorf("message dropped the original text %q: %q", tc.err.Error(), msg)
			}
			if !errors.Is(got, tc.err) {
				t.Errorf("annotation broke the error chain: %v does not wrap %v", got, tc.err)
			}
		})
		t.Run(tc.name+" before the deadline is unchanged", func(t *testing.T) {
			got := annotateTransferTimeout(tc.err, deadline, deadline-time.Millisecond)
			if got != tc.err { //nolint:errorlint // identity is the assertion
				t.Fatalf("annotateTransferTimeout rewrote an early connection loss: %v", got)
			}
		})
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"context deadline", context.DeadlineExceeded},
		{"context canceled", context.Canceled},
		{"server error", &mysql.MySQLError{Number: 1105, Message: "remote origin not found"}},
		{"unrelated", errors.New("remote origin not found")},
	} {
		t.Run(tc.name+" past the deadline is unchanged", func(t *testing.T) {
			got := annotateTransferTimeout(tc.err, deadline, 2*deadline)
			if got != tc.err { //nolint:errorlint // identity is the assertion
				t.Fatalf("annotateTransferTimeout rewrote a non-connection error: %v", got)
			}
		})
	}
}

// TestTransferDropBeforeDeadlineIsNotAnnotated pins the elapsed half of the
// gate at a real transfer site: a server that closes the connection on the
// CALL long before the 30s deadline yields the driver's ErrInvalidConn as-is,
// with no claim that a deadline fired.
func TestTransferDropBeforeDeadlineIsNotAnnotated(t *testing.T) {
	t.Setenv("BEADS_DOLT_TRANSFER_TIMEOUT", "30s")
	s := stallingStore(t, map[string]fakeStep{"CALL DOLT_PUSH": dropStep})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
	elapsed := time.Since(start)
	if !errors.Is(err, mysql.ErrInvalidConn) {
		t.Fatalf("dropped CALL returned %v, want mysql.ErrInvalidConn", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("dropped CALL took %v; the server closed the connection at once", elapsed.Round(time.Millisecond))
	}
	if err != mysql.ErrInvalidConn { //nolint:errorlint // identity is the assertion
		t.Errorf("early connection loss was rewritten: %v", err)
	}
	if strings.Contains(err.Error(), "BEADS_DOLT_TRANSFER_TIMEOUT") || strings.Contains(err.Error(), "read deadline") {
		t.Errorf("early connection loss claims a deadline fired: %v", err)
	}
}

// fakeStep is how startStallingMySQL answers one statement. The zero value
// stalls: the server holds the statement unanswered until the client closes.
type fakeStep struct {
	drop    bool   // close the connection instead of answering
	ok      bool   // answer with an OK packet
	errCode uint16 // non-zero: answer with this server error
	errMsg  string
}

var (
	stallStep = fakeStep{}
	dropStep  = fakeStep{drop: true}
	okStep    = fakeStep{ok: true}
)

func errStep(code uint16, msg string) fakeStep { return fakeStep{errCode: code, errMsg: msg} }

// stallingStore returns a DoltStore whose connStr points at a
// startStallingMySQL server running script. The transfer sites under test
// read only connStr and branch.
func stallingStore(t *testing.T, script map[string]fakeStep) *DoltStore {
	t.Helper()
	port := startStallingMySQL(t, script)
	return &DoltStore{
		connStr: buildServerDSN(&Config{ServerHost: "127.0.0.1", ServerPort: port, ServerUser: "root"}, "beads"),
		branch:  "main",
	}
}

// startStallingMySQL serves just enough of the MySQL protocol for
// go-sql-driver to connect (modelled on uow's serveMinimalMySQL): a greeting,
// OK to the auth response, and OK to the session statements a transfer site
// issues around its CALL (START TRANSACTION, SET, COMMIT, ROLLBACK). Every
// other COM_QUERY is answered by the first script entry whose key prefixes
// the statement, or stalls when none does. The server sets no deadline of its
// own, so a stall lasts until the client gives up.
func startStallingMySQL(t *testing.T, script map[string]fakeStep) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func() { defer wg.Done(); serveStallingMySQL(conn, script) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func serveStallingMySQL(conn net.Conn, script map[string]fakeStep) {
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
	read := func() (byte, []byte, error) {
		var hdr [4]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return 0, nil, err
		}
		body := make([]byte, int(hdr[0])|int(hdr[1])<<8|int(hdr[2])<<16)
		_, err := io.ReadFull(conn, body)
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
		step := okStep
		if body[0] == 0x03 { // COM_QUERY
			step = stepFor(strings.TrimSpace(string(body[1:])), script)
		}
		switch {
		case step.drop:
			return
		case step.ok:
			if write(seq+1, ok) != nil {
				return
			}
		case step.errCode != 0:
			pkt := []byte{0xff}
			pkt = binary.LittleEndian.AppendUint16(pkt, step.errCode)
			pkt = append(pkt, "#HY000"...)
			pkt = append(pkt, step.errMsg...)
			if write(seq+1, pkt) != nil {
				return
			}
		default: // stall until the client closes the connection
			_, _ = io.Copy(io.Discard, conn)
			return
		}
	}
}

func stepFor(query string, script map[string]fakeStep) fakeStep {
	upper := strings.ToUpper(query)
	for _, session := range []string{"START TRANSACTION", "BEGIN", "SET ", "COMMIT", "ROLLBACK"} {
		if strings.HasPrefix(upper, session) {
			return okStep
		}
	}
	for prefix, step := range script {
		if strings.HasPrefix(upper, strings.ToUpper(prefix)) {
			return step
		}
	}
	return stallStep
}
