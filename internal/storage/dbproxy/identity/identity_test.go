package identity

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootID_StableAcrossSymlink(t *testing.T) {
	target := t.TempDir()
	first, err := RootID(target)
	require.NoError(t, err)
	second, err := RootID(target)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	link := filepath.Join(t.TempDir(), "workspace-link")
	require.NoError(t, os.Symlink(target, link))
	linked, err := RootID(link)
	require.NoError(t, err)
	assert.Equal(t, first, linked)
}

func TestIdentReplyMACRejectsTampering(t *testing.T) {
	reply := IdentReply{
		Schema:      2,
		Role:        "proxy",
		RootID:      "root",
		UpstreamID:  "upstream",
		PID:         123,
		Birth:       "birth",
		DataPort:    3306,
		ControlPort: 3307,
	}
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	nonce := "0123456789abcdef0123456789abcdef"
	signed, err := SignIdentReply(reply, secret, nonce)
	require.NoError(t, err)
	require.NoError(t, VerifyIdentReply(signed, secret, nonce))

	signed.DataPort++
	require.ErrorContains(t, VerifyIdentReply(signed, secret, nonce), "authentication failed")
}

func TestIdentRequestMACAuthenticatesNonce(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	nonce := "0123456789abcdef0123456789abcdef"
	mac, err := IdentRequestMAC(secret, nonce)
	require.NoError(t, err)

	nonceBytes, err := hex.DecodeString(nonce)
	require.NoError(t, err)
	wantH := hmac.New(sha256.New, []byte(secret))
	_, _ = wantH.Write([]byte("bd-proxy-ident-request-v1\x00"))
	_, _ = wantH.Write(nonceBytes)
	assert.Equal(t, hex.EncodeToString(wantH.Sum(nil)), mac)

	rawH := hmac.New(sha256.New, []byte(secret))
	_, _ = rawH.Write(nonceBytes)
	assert.NotEqual(t, hex.EncodeToString(rawH.Sum(nil)), mac)

	assert.True(t, VerifyIdentRequest(secret, nonce, mac))
	assert.False(t, VerifyIdentRequest("wrong secret", nonce, mac))
	assert.False(t, VerifyIdentRequest(secret, "invalid", mac))
	_, err = IdentRequestMAC(secret, "invalid")
	require.Error(t, err)
}

func TestDataAuthToken(t *testing.T) {
	first := DataAuthToken("shared secret")
	assert.Len(t, first, 64)
	assert.Equal(t, first, DataAuthToken("shared secret"))
	assert.NotEqual(t, first, DataAuthToken("different secret"))
	portA := DataAuthTokenForPort("shared secret", 3306)
	portB := DataAuthTokenForPort("shared secret", 3307)
	assert.Len(t, portA, 64)
	assert.NotEqual(t, first, portA)
	assert.NotEqual(t, portA, portB)
}

func TestIdentifyRejectsUnauthenticatedReply(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	serverErr := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer conn.Close()
		if _, readErr := bufio.NewReader(conn).ReadString('\n'); readErr != nil {
			serverErr <- readErr
			return
		}
		reply := IdentReply{Schema: 2, Role: "proxy", MAC: "00"}
		serverErr <- json.NewEncoder(conn).Encode(reply)
	}()

	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err = Identify("127.0.0.1", port, secret, time.Second)
	require.ErrorContains(t, err, "authentication failed")
	require.NoError(t, <-serverErr)
}

func TestIdentifyRequestCarriesFreshNonce(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	type request struct {
		line  string
		mac   string
		nonce string
	}
	requests := make(chan request, 2)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	for range 2 {
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			defer conn.Close()
			line, readErr := bufio.NewReader(conn).ReadString('\n')
			if readErr != nil {
				return
			}
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) != 3 || fields[0] != "IDENT" {
				return
			}
			requests <- request{line: line, mac: fields[1], nonce: fields[2]}
			reply, signErr := SignIdentReply(IdentReply{Schema: 2, Role: "proxy"}, secret, fields[2])
			if signErr == nil {
				_ = json.NewEncoder(conn).Encode(reply)
			}
		}()
	}
	for range 2 {
		_, err := Identify("127.0.0.1", port, secret, time.Second)
		require.NoError(t, err)
	}
	first := <-requests
	second := <-requests
	assert.NotContains(t, first.line, secret)
	assert.NotContains(t, second.line, secret)
	assert.True(t, VerifyIdentRequest(secret, first.nonce, first.mac))
	assert.True(t, VerifyIdentRequest(secret, second.nonce, second.mac))
	assert.Len(t, first.nonce, identNonceBytes*2)
	assert.Len(t, second.nonce, identNonceBytes*2)
	assert.NotEqual(t, first.nonce, second.nonce)
}

func TestSecret_WriteReadAndRotate(t *testing.T) {
	root := t.TempDir()
	first, err := WriteSecret(root)
	require.NoError(t, err)
	got, err := ReadSecret(root)
	require.NoError(t, err)
	assert.Equal(t, first, got)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, SecretFileName))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	second, err := WriteSecret(root)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	got, err = ReadSecret(root)
	require.NoError(t, err)
	assert.Equal(t, second, got)
}

func TestReadSecret_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "short", value: "0123"},
		{name: "non hex", value: "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, SecretFileName), []byte(tc.value), 0o600))
			_, err := ReadSecret(root)
			require.Error(t, err)
		})
	}
}
