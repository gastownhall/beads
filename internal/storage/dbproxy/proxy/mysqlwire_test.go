package proxy

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLenencIntRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 250, 251, 0xfb, 0xfc, 0xffff, 0x10000, 0xffffff, 0x1000000, 1 << 40} {
		b := appendLenencInt(nil, v)
		got, next, err := readLenencInt(b, 0)
		require.NoError(t, err, "v=%d", v)
		require.Equal(t, v, got, "v=%d", v)
		require.Equal(t, len(b), next, "v=%d consumed all bytes", v)
	}
}

func TestPacketFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := bytes.Repeat([]byte{0xab}, 100)
	require.NoError(t, writePacket(&buf, 7, payload))
	got, seq, err := readPacket(&buf)
	require.NoError(t, err)
	require.Equal(t, byte(7), seq)
	require.Equal(t, payload, got)
}

func TestServerGreetingParsesBack(t *testing.T) {
	// A greeting the proxy emits to clients must be parseable by our own
	// backend-side parser (the two are symmetric on salt + plugin).
	salt := makeSalt(42)
	g := serverGreeting(42, salt)
	parsedSalt, plugin, err := parseServerGreeting(g)
	require.NoError(t, err)
	require.Equal(t, mysqlNativePassword, plugin)
	require.Equal(t, salt, parsedSalt, "round-tripped salt must match")
}

func TestParseClientHandshakeResponse(t *testing.T) {
	// Build a HandshakeResponse41 the way a backend handshake would and parse
	// it as the server side would.
	caps := capProtocol41 | capSecureConnection | capPluginAuth | capConnectWithDB
	resp := buildHandshakeResponse41(caps, "mydb", "root", nil, mysqlNativePassword)
	h, err := parseClientHandshakeResponse(resp)
	require.NoError(t, err)
	require.Equal(t, "root", h.username)
	require.Equal(t, "mydb", h.database)
	require.NotZero(t, h.capabilities&capProtocol41)
	require.NotZero(t, h.capabilities&capConnectWithDB)
}

func TestParseClientHandshakeResponse_NoDB(t *testing.T) {
	caps := capProtocol41 | capSecureConnection | capPluginAuth
	resp := buildHandshakeResponse41(caps, "", "someuser", []byte{1, 2, 3, 4}, mysqlNativePassword)
	h, err := parseClientHandshakeResponse(resp)
	require.NoError(t, err)
	require.Equal(t, "someuser", h.username)
	require.Equal(t, "", h.database)
	require.Zero(t, h.capabilities&capConnectWithDB)
}

func TestNativeScrambleEmptyPassword(t *testing.T) {
	require.Nil(t, scramble(mysqlNativePassword, "", makeSalt(1)))
}

func TestNativeScrambleDeterministic(t *testing.T) {
	salt := makeSalt(99)
	a := nativeScramble("hunter2", salt)
	b := nativeScramble("hunter2", salt)
	require.Equal(t, a, b)
	require.Len(t, a, 20) // SHA1 digest length
	require.NotEqual(t, nativeScramble("other", salt), a)
}

func TestOKPacketShapes(t *testing.T) {
	// PROTOCOL_41 OK has status(2)+warnings(2) after the two lenenc ints.
	ok41 := okPacket(capProtocol41)
	require.Equal(t, byte(0x00), ok41[0])
	require.Len(t, ok41, 1+1+1+2+2)

	// No PROTOCOL_41, no TRANSACTIONS: just header + two lenenc ints.
	okPlain := okPacket(0)
	require.Len(t, okPlain, 1+1+1)
}
