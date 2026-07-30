package identity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	nonces := make(chan string, 2)
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
			var command, gotSecret, nonce string
			if _, scanErr := fmt.Sscanf(line, "%s %s %s\n", &command, &gotSecret, &nonce); scanErr != nil {
				return
			}
			nonces <- nonce
			reply, signErr := SignIdentReply(IdentReply{Schema: 2, Role: "proxy"}, secret, nonce)
			if signErr == nil {
				_ = json.NewEncoder(conn).Encode(reply)
			}
		}()
	}
	for range 2 {
		_, err := Identify("127.0.0.1", port, secret, time.Second)
		require.NoError(t, err)
	}
	first := <-nonces
	second := <-nonces
	assert.Len(t, first, identNonceBytes*2)
	assert.Len(t, second, identNonceBytes*2)
	assert.NotEqual(t, first, second)
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
