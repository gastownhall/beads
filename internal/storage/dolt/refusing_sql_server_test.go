package dolt

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

// refusedCallMessage is the error a refusingSQLServer returns for every
// statement it refuses, the way a server whose remote rejects stale
// credentials fails a CALL DOLT_* transfer.
const refusedCallMessage = "fake dolt server: transfer refused: AccessDenied"

// refusingSQLServer starts a MySQL-protocol server on loopback that accepts
// any login, acknowledges transaction control and SET statements, and fails
// every other statement (and every prepare) with error 1105
// refusedCallMessage. It stands in for a reachable Dolt server whose transfer
// fails after the connection was set up, so a test can tell the transfer's
// own failure from a failure to reach the server. It returns a DSN for it.
func refusingSQLServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveRefusingSQLConn(c)
		}
	}()
	return "root@tcp(" + ln.Addr().String() + ")/beads"
}

func serveRefusingSQLConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	const (
		clientLongPassword     = 0x00000001
		clientLongFlag         = 0x00000004
		clientConnectWithDB    = 0x00000008
		clientProtocol41       = 0x00000200
		clientTransactions     = 0x00002000
		clientSecureConnection = 0x00008000
		clientPluginAuth       = 0x00080000
	)
	caps := uint32(clientLongPassword | clientLongFlag | clientConnectWithDB | clientProtocol41 |
		clientTransactions | clientSecureConnection | clientPluginAuth)
	salt := []byte("abcdefghijklmnopqrst")

	hs := []byte{10}
	hs = append(hs, "8.0.33-fake"...)
	hs = append(hs, 0)
	hs = binary.LittleEndian.AppendUint32(hs, 1)
	hs = append(hs, salt[:8]...)
	hs = append(hs, 0)
	hs = binary.LittleEndian.AppendUint16(hs, uint16(caps))
	hs = append(hs, 45)                                // utf8mb4_general_ci
	hs = binary.LittleEndian.AppendUint16(hs, 0x0002) // SERVER_STATUS_AUTOCOMMIT
	hs = binary.LittleEndian.AppendUint16(hs, uint16(caps>>16))
	hs = append(hs, byte(len(salt)+1))
	hs = append(hs, make([]byte, 10)...)
	hs = append(hs, salt[8:]...)
	hs = append(hs, 0)
	hs = append(hs, "mysql_native_password"...)
	hs = append(hs, 0)
	if writeSQLPacket(c, 0, hs) != nil {
		return
	}
	if _, _, err := readSQLPacket(r); err != nil { // handshake response: any login
		return
	}
	ok := []byte{0x00, 0, 0, 0x02, 0, 0, 0}
	if writeSQLPacket(c, 2, ok) != nil {
		return
	}
	refuse := []byte{0xff}
	refuse = binary.LittleEndian.AppendUint16(refuse, 1105)
	refuse = append(refuse, "#HY000"...)
	refuse = append(refuse, refusedCallMessage...)
	for {
		seq, body, err := readSQLPacket(r)
		if err != nil || len(body) == 0 {
			return
		}
		var reply []byte
		switch body[0] {
		case 0x01: // COM_QUIT
			return
		case 0x0e: // COM_PING
			reply = ok
		case 0x03: // COM_QUERY
			q := strings.ToUpper(strings.TrimSpace(string(body[1:])))
			switch {
			case strings.HasPrefix(q, "START TRANSACTION"), strings.HasPrefix(q, "BEGIN"),
				strings.HasPrefix(q, "COMMIT"), strings.HasPrefix(q, "ROLLBACK"), strings.HasPrefix(q, "SET "):
				reply = ok
			default:
				reply = refuse
			}
		default: // COM_STMT_PREPARE and anything else
			reply = refuse
		}
		if writeSQLPacket(c, seq+1, reply) != nil {
			return
		}
	}
}

func readSQLPacket(r *bufio.Reader) (byte, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr[3], body, nil
}

func writeSQLPacket(w io.Writer, seq byte, body []byte) error {
	n := len(body)
	pkt := append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, body...)
	_, err := w.Write(pkt)
	return err
}
