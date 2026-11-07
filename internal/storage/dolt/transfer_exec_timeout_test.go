package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// TestTransferExecTimeoutDuration pins how BEADS_DOLT_TRANSFER_TIMEOUT
// resolves (GH#6375): positive duration strings and bare seconds are honored;
// unset, unparsable and non-positive values keep the 5m default.
func TestTransferExecTimeoutDuration(t *testing.T) {
	if transferExecTimeoutEnv != "BEADS_DOLT_TRANSFER_TIMEOUT" {
		t.Fatalf("transferExecTimeoutEnv = %q, want BEADS_DOLT_TRANSFER_TIMEOUT", transferExecTimeoutEnv)
	}
	if transferExecTimeout != 5*time.Minute {
		t.Fatalf("transferExecTimeout = %v, want 5m (the default must not change)", transferExecTimeout)
	}
	tests := []struct {
		name  string
		value string
		set   bool
		want  time.Duration
	}{
		{name: "unset", want: transferExecTimeout},
		{name: "empty", value: "", set: true, want: transferExecTimeout},
		{name: "duration string", value: "20m", set: true, want: 20 * time.Minute},
		{name: "bare seconds", value: "90", set: true, want: 90 * time.Second},
		{name: "surrounding whitespace", value: "  45s \t", set: true, want: 45 * time.Second},
		{name: "invalid", value: "soon", set: true, want: transferExecTimeout},
		{name: "zero", value: "0", set: true, want: transferExecTimeout},
		{name: "zero duration", value: "0s", set: true, want: transferExecTimeout},
		{name: "negative duration", value: "-5s", set: true, want: transferExecTimeout},
		{name: "negative bare seconds", value: "-30", set: true, want: transferExecTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(transferExecTimeoutEnv, tt.value)
			} else {
				unsetEnvForTest(t, transferExecTimeoutEnv)
			}
			if got := transferExecTimeoutDuration(); got != tt.want {
				t.Fatalf("transferExecTimeoutDuration() with %s=%q = %v, want %v", transferExecTimeoutEnv, tt.value, got, tt.want)
			}
		})
	}
}

// TestAnnotateTransferTimeout pins when a transfer failure gets the deadline
// hint: only for the driver's ErrInvalidConn after at least the captured
// deadline has elapsed. Everything else passes through as the identical error.
func TestAnnotateTransferTimeout(t *testing.T) {
	const deadline = 90 * time.Second
	sqlErr := &mysql.MySQLError{Number: 1105, Message: "remote rejected the push"}
	wrappedInvalid := fmt.Errorf("exec: %w", mysql.ErrInvalidConn)
	canceled := fmt.Errorf("exec: %w", context.Canceled)

	passThrough := []struct {
		name    string
		err     error
		elapsed time.Duration
	}{
		{name: "success", err: nil, elapsed: 2 * deadline},
		{name: "ordinary error", err: errors.New("boom"), elapsed: 2 * deadline},
		{name: "sql error", err: sqlErr, elapsed: 2 * deadline},
		{name: "invalid conn before deadline", err: mysql.ErrInvalidConn, elapsed: deadline - time.Millisecond},
		{name: "immediate reset", err: wrappedInvalid, elapsed: 0},
		{name: "canceled context", err: canceled, elapsed: 2 * deadline},
		{name: "expired context", err: context.DeadlineExceeded, elapsed: 2 * deadline},
	}
	for _, tt := range passThrough {
		t.Run(tt.name, func(t *testing.T) {
			if got := annotateTransferTimeout(tt.err, tt.elapsed, deadline); got != tt.err {
				t.Fatalf("annotateTransferTimeout(%v, %v, %v) = %v, want the identical error back", tt.err, tt.elapsed, deadline, got)
			}
		})
	}

	annotated := []struct {
		name    string
		err     error
		elapsed time.Duration
	}{
		{name: "sentinel at deadline", err: mysql.ErrInvalidConn, elapsed: deadline},
		{name: "sentinel past deadline", err: mysql.ErrInvalidConn, elapsed: deadline + time.Second},
		{name: "wrapped sentinel past deadline", err: wrappedInvalid, elapsed: 3 * deadline},
	}
	for _, tt := range annotated {
		t.Run(tt.name, func(t *testing.T) {
			got := annotateTransferTimeout(tt.err, tt.elapsed, deadline)
			if got == nil || got == tt.err {
				t.Fatalf("annotateTransferTimeout(%v, %v, %v) = %v, want an annotated error", tt.err, tt.elapsed, deadline, got)
			}
			if !errors.Is(got, mysql.ErrInvalidConn) || !errors.Is(got, tt.err) {
				t.Fatalf("annotated error %q no longer wraps %v", got, tt.err)
			}
			msg := got.Error()
			for _, want := range []string{"read deadline (1m30s) may have expired", transferExecTimeoutEnv + "=", tt.err.Error()} {
				if !strings.Contains(msg, want) {
					t.Fatalf("annotated error %q does not contain %q", msg, want)
				}
			}
			// The hint is advice, not a diagnosis: a reset after the deadline
			// is still possible, so it must not claim the deadline fired.
			if strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded") {
				t.Fatalf("annotated error %q asserts a timeout it cannot prove", msg)
			}
		})
	}
}

// transferFakeReply is how a fakeTransferServer answers one COM_QUERY.
type transferFakeReply int

const (
	// replyOK answers with an OK packet (no result set).
	replyOK transferFakeReply = iota
	// replySilent never answers: the server keeps reading until the client
	// drops the connection, as a server stuck in a long transfer does.
	replySilent
	// replyBranchTrackingError answers with the error DOLT_PULL returns when
	// the branch has no upstream tracking config (GH#3144's fallback trigger).
	replyBranchTrackingError
)

// fakeTransferServer is a loopback listener that speaks just enough of the
// MySQL protocol for a one-shot transfer connection: it accepts any login,
// answers session setup (START TRANSACTION, SET, SELECT, COMMIT) with OK, and
// answers each query as script says. The client must use interpolateParams
// so every statement arrives as a COM_QUERY. Silent statements are reported
// on reached.
type fakeTransferServer struct {
	ln      net.Listener
	script  func(query string) transferFakeReply
	reached chan string

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
	wg     sync.WaitGroup
}

// newFakeTransferServer starts a fakeTransferServer on 127.0.0.1. Cleanup
// closes the listener and every accepted socket and joins all goroutines.
func newFakeTransferServer(t *testing.T, script func(query string) transferFakeReply) *fakeTransferServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeTransferServer{ln: ln, script: script, reached: make(chan string, 16)}
	f.wg.Add(1)
	go f.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		f.closed = true
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.wg.Wait()
	})
	return f
}

// dsn is a DSN for f; interpolateParams keeps CALLs with args off the
// prepared-statement protocol, which the fake does not speak.
func (f *fakeTransferServer) dsn() string {
	return "root@tcp(" + f.ln.Addr().String() + ")/beads?interpolateParams=true"
}

// silentStatement returns the silent statement the client reached, or "".
func (f *fakeTransferServer) silentStatement() string {
	select {
	case q := <-f.reached:
		return q
	default:
		return ""
	}
}

func (f *fakeTransferServer) serve() {
	defer f.wg.Done()
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			_ = conn.Close()
			return
		}
		f.conns = append(f.conns, conn)
		f.wg.Add(1)
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeTransferServer) handle(conn net.Conn) {
	defer f.wg.Done()
	defer conn.Close()
	// CLIENT_LONG_PASSWORD | CONNECT_WITH_DB | PROTOCOL_41 | TRANSACTIONS |
	// SECURE_CONNECTION | PLUGIN_AUTH
	const caps = 0x1 | 0x8 | 0x200 | 0x2000 | 0x8000 | 0x80000
	scramble := []byte("abcdefghijklmnopqrst")
	var hs bytes.Buffer
	hs.WriteByte(10)
	hs.WriteString("8.0.33-fake\x00")
	_ = binary.Write(&hs, binary.LittleEndian, uint32(1))
	hs.Write(scramble[:8])
	hs.WriteByte(0)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps&0xffff))
	hs.WriteByte(0x21)
	_ = binary.Write(&hs, binary.LittleEndian, uint16(2))
	_ = binary.Write(&hs, binary.LittleEndian, uint16(caps>>16))
	hs.WriteByte(21)
	hs.Write(make([]byte, 10))
	hs.Write(scramble[8:])
	hs.WriteByte(0)
	hs.WriteString("mysql_native_password\x00")
	if writeFakeMySQLPacket(conn, 0, hs.Bytes()) != nil {
		return
	}
	if _, err := readFakeMySQLPacket(conn); err != nil {
		return
	}
	if writeFakeMySQLPacket(conn, 2, fakeMySQLOK) != nil {
		return
	}
	for {
		payload, err := readFakeMySQLPacket(conn)
		if err != nil || len(payload) == 0 {
			return
		}
		switch payload[0] {
		case 0x01: // COM_QUIT
			return
		case 0x03: // COM_QUERY
		default:
			if writeFakeMySQLPacket(conn, 1, fakeMySQLError("unsupported command (fake)")) != nil {
				return
			}
			continue
		}
		query := string(payload[1:])
		switch f.script(query) {
		case replySilent:
			select {
			case f.reached <- query:
			default:
			}
			_, _ = io.Copy(io.Discard, conn) // until the client gives up
			return
		case replyBranchTrackingError:
			err = writeFakeMySQLPacket(conn, 1, fakeMySQLError("You must specify a branch; you did not specify a branch to pull from"))
		default:
			err = writeFakeMySQLPacket(conn, 1, fakeMySQLOK)
		}
		if err != nil {
			return
		}
	}
}

// fakeMySQLOK is an OK packet: no rows affected, no insert id, autocommit.
var fakeMySQLOK = []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}

func fakeMySQLError(msg string) []byte {
	var b bytes.Buffer
	b.WriteByte(0xff)
	_ = binary.Write(&b, binary.LittleEndian, uint16(1105))
	b.WriteString("#HY000" + msg)
	return b.Bytes()
}

func writeFakeMySQLPacket(w io.Writer, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(header, payload...))
	return err
}

func readFakeMySQLPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return payload, err
}

// silentOn scripts a fake that withholds the reply to the first statement
// containing silent, answers each statement containing a key of errs with
// that reply, and answers everything else with OK.
func silentOn(silent string, errs map[string]transferFakeReply) func(string) transferFakeReply {
	return func(query string) transferFakeReply {
		upper := strings.ToUpper(query)
		for key, reply := range errs {
			if strings.Contains(upper, key) {
				return reply
			}
		}
		if strings.Contains(upper, silent) {
			return replySilent
		}
		return replyOK
	}
}

// newFakeTransferStore is a DoltStore whose connections all reach f. s.db is
// set too, for the remote lookups a transfer route makes before its CALL.
func newFakeTransferStore(t *testing.T, f *fakeTransferServer) *DoltStore {
	t.Helper()
	db, err := sql.Open("mysql", f.dsn())
	if err != nil {
		t.Fatalf("open fake pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db, connStr: f.dsn(), branch: "main"}
}

// TestTransferReadDeadlineBoundsTransfer reproduces GH#6375 end to end through
// the real driver: every SQL transfer route must run on a connection whose
// read deadline is BEADS_DOLT_TRANSFER_TIMEOUT, and the failure must say so.
// At base the deadline is a hardcoded 5m, so the silent statement hangs until
// the safety context expires instead.
func TestTransferReadDeadlineBoundsTransfer(t *testing.T) {
	branchTracking := map[string]transferFakeReply{"DOLT_PULL": replyBranchTrackingError}
	tests := []struct {
		name   string
		silent string
		errs   map[string]transferFakeReply
		run    func(ctx context.Context, s *DoltStore) error
	}{
		{
			name:   "sql push",
			silent: "DOLT_PUSH",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", "origin", "main")
			},
		},
		{
			name:   "federation push",
			silent: "DOLT_PUSH",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.execWithLongTimeout(ctx, "CALL DOLT_PUSH(?, ?)", "peer", "main")
			},
		},
		{
			name:   "federation fetch",
			silent: "DOLT_FETCH",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.execWithLongTimeout(ctx, "CALL DOLT_FETCH(?)", "peer")
			},
		},
		{
			name:   "pull",
			silent: "DOLT_PULL",
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.pullWithAutoResolveUnchecked(ctx, "origin", "CALL DOLT_PULL(?, ?)", "origin", "main")
				return err
			},
		},
		{
			name:   "pull fallback fetch",
			silent: "DOLT_FETCH",
			errs:   branchTracking,
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.pullWithAutoResolveUnchecked(ctx, "origin", "CALL DOLT_PULL(?, ?)", "origin", "main")
				return err
			},
		},
		{
			name:   "pull fallback merge",
			silent: "DOLT_MERGE",
			errs:   branchTracking,
			run: func(ctx context.Context, s *DoltStore) error {
				_, err := s.pullWithAutoResolveUnchecked(ctx, "origin", "CALL DOLT_PULL(?, ?)", "origin", "main")
				return err
			},
		},
		{
			name:   "tracking ref fetch",
			silent: "DOLT_FETCH",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.refreshTrackingRef(ctx, "origin")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transferExecTimeoutEnv, "1s")
			f := newFakeTransferServer(t, silentOn(tt.silent, tt.errs))
			s := newFakeTransferStore(t, f)

			// The safety context is what ends the call when the knob is ignored.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := tt.run(ctx, s)

			if got := f.silentStatement(); !strings.Contains(strings.ToUpper(got), tt.silent) {
				t.Fatalf("the transfer never reached the silent %s statement (reached %q); err: %v", tt.silent, got, err)
			}
			if ctx.Err() != nil {
				t.Fatalf("the safety context expired first: the transfer connection ignored %s=1s (err: %v)", transferExecTimeoutEnv, err)
			}
			if !errors.Is(err, mysql.ErrInvalidConn) {
				t.Fatalf("err = %v, want it to wrap mysql.ErrInvalidConn", err)
			}
			for _, want := range []string{"read deadline (1s)", transferExecTimeoutEnv} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, want it to name %q", err, want)
				}
			}
		})
	}
}
