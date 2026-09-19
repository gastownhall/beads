package identity

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	maxIdentReplyBytes = 4096
	identNonceBytes    = 16
)

// ErrIdentRefused reports that a control listener closed the connection
// without replying to an identity request.
var ErrIdentRefused = errors.New("identity: request refused")

// IdentReply is the authenticated identity published by a managed proxy.
type IdentReply struct {
	Schema      int    `json:"schema"`
	Role        string `json:"role"`
	RootID      string `json:"root_id"`
	UpstreamID  string `json:"upstream_id"`
	PID         int    `json:"pid"`
	Birth       string `json:"birth"`
	DataPort    int    `json:"data_port"`
	ControlPort int    `json:"control_port"`
	MAC         string `json:"mac"`
}

// Identify authenticates to a proxy control listener and returns its identity.
func Identify(host string, controlPort int, secret string, timeout time.Duration) (*IdentReply, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(controlPort))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("identity: dial control listener: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("identity: set control deadline: %w", err)
	}
	nonceBytes := make([]byte, identNonceBytes)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("identity: generate request nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	if _, err := io.WriteString(conn, "IDENT "+secret+" "+nonce+"\n"); err != nil {
		return nil, fmt.Errorf("identity: write request: %w", err)
	}

	line, err := bufio.NewReader(io.LimitReader(conn, maxIdentReplyBytes+1)).ReadString('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return nil, ErrIdentRefused
	}
	if err != nil {
		return nil, fmt.Errorf("identity: read reply: %w", err)
	}
	if len(line) > maxIdentReplyBytes {
		return nil, errors.New("identity: oversized reply")
	}

	var reply IdentReply
	if err := json.Unmarshal([]byte(line), &reply); err != nil {
		return nil, fmt.Errorf("identity: decode reply: %w", err)
	}
	if err := VerifyIdentReply(reply, secret, nonce); err != nil {
		return nil, err
	}
	return &reply, nil
}

// SignIdentReply authenticates reply for the nonce in an IDENT request.
// The MAC covers the raw nonce bytes followed by canonical JSON for every
// reply field except MAC.
func SignIdentReply(reply IdentReply, secret, nonce string) (IdentReply, error) {
	nonceBytes, err := decodeIdentNonce(nonce)
	if err != nil {
		return IdentReply{}, err
	}
	payload, err := canonicalIdentReply(reply)
	if err != nil {
		return IdentReply{}, err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(nonceBytes)
	_, _ = mac.Write(payload)
	reply.MAC = hex.EncodeToString(mac.Sum(nil))
	return reply, nil
}

// VerifyIdentReply verifies the authenticated reply for the request nonce.
func VerifyIdentReply(reply IdentReply, secret, nonce string) error {
	got, err := hex.DecodeString(reply.MAC)
	if err != nil {
		return errors.New("identity: invalid reply MAC")
	}
	signed, err := SignIdentReply(reply, secret, nonce)
	if err != nil {
		return fmt.Errorf("identity: authenticate reply: %w", err)
	}
	want, err := hex.DecodeString(signed.MAC)
	if err != nil {
		return fmt.Errorf("identity: decode expected reply MAC: %w", err)
	}
	if !hmac.Equal(got, want) {
		return errors.New("identity: reply authentication failed")
	}
	return nil
}

func decodeIdentNonce(nonce string) ([]byte, error) {
	raw, err := hex.DecodeString(nonce)
	if err != nil || len(raw) != identNonceBytes {
		return nil, errors.New("identity: invalid request nonce")
	}
	return raw, nil
}

func canonicalIdentReply(reply IdentReply) ([]byte, error) {
	payload := struct {
		Schema      int    `json:"schema"`
		Role        string `json:"role"`
		RootID      string `json:"root_id"`
		UpstreamID  string `json:"upstream_id"`
		PID         int    `json:"pid"`
		Birth       string `json:"birth"`
		DataPort    int    `json:"data_port"`
		ControlPort int    `json:"control_port"`
	}{
		Schema:      reply.Schema,
		Role:        reply.Role,
		RootID:      reply.RootID,
		UpstreamID:  reply.UpstreamID,
		PID:         reply.PID,
		Birth:       reply.Birth,
		DataPort:    reply.DataPort,
		ControlPort: reply.ControlPort,
	}
	return json.Marshal(payload)
}
