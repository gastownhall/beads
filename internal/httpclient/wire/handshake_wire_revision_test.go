// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// contextBodyWithWireRevision is contextBody plus the two wire-shape members
// task #2 reads. wireRevision/minClientWireRevision of 0 renders the member
// omitted entirely rather than a literal 0, matching what a pre-#6053 server
// (one that predates ContextResponse.wire_revision) actually sends — the
// permanently-retired-value rule (ContextResponse.WireRevision's doc) means a
// server that HAS the field never legitimately sends a literal 0 either, so
// this helper cannot accidentally construct a body a real server would.
func contextBodyWithWireRevision(apiVersion, projectID string, wireRevision, minClientWireRevision int, capabilities ...string) string {
	base := contextBody(apiVersion, projectID, capabilities...)
	// contextBody always closes with `"schema_version":7}`; splice the two
	// extra members in before the closing brace rather than hand-rolling the
	// whole document a second time.
	const suffix = `"schema_version":7}`
	if len(base) < len(suffix) || base[len(base)-len(suffix):] != suffix {
		panic("contextBody shape changed; update contextBodyWithWireRevision")
	}
	head := base[:len(base)-1] // drop the closing brace
	extra := ""
	if wireRevision != 0 {
		extra += `,"wire_revision":` + strconv.Itoa(wireRevision)
	}
	if minClientWireRevision != 0 {
		extra += `,"min_client_wire_revision":` + strconv.Itoa(minClientWireRevision)
	}
	return head + extra + "}"
}

func TestHandshakeAcceptsAnOmittedWireRevision(t *testing.T) {
	// A server that predates the field omits it; the decoded zero value must
	// read as "in range," never as a skew to refuse.
	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBodyWithWireRevision("v0", "proj-1", 0, 0, "issues.list")))
	if _, err := c.Handshake(ctx(t)); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
}

func TestHandshakeAcceptsTheClientsOwnWireRevisionExactly(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBodyWithWireRevision("v0", "proj-1", ClientWireRevision, 0, "issues.list")))
	if _, err := c.Handshake(ctx(t)); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
}

func TestHandshakeRefusesWhenTheServersFloorExceedsThisClient(t *testing.T) {
	// min_client_wire_revision above what this build speaks: the server
	// requires a newer client than this one.
	c, _ := newTestClient(t, Options{}, nil,
		serveContext(contextBodyWithWireRevision("v0", "proj-1", ClientWireRevision, ClientWireRevision+1, "issues.list")))
	_, err := c.Handshake(ctx(t))
	if !errors.Is(err, ErrWireRevisionSkew) {
		t.Fatalf("err = %v, want ErrWireRevisionSkew", err)
	}
	var skew *WireRevisionSkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err is %T, want *WireRevisionSkewError", err)
	}
	if skew.MinClientWireRevision != ClientWireRevision+1 {
		t.Errorf("MinClientWireRevision = %d, want %d", skew.MinClientWireRevision, ClientWireRevision+1)
	}
	if skew.ClientWireRevision != ClientWireRevision {
		t.Errorf("ClientWireRevision = %d, want %d", skew.ClientWireRevision, ClientWireRevision)
	}
	msg := err.Error()
	if !strings.Contains(msg, strconv.Itoa(ClientWireRevision+1)) {
		t.Errorf("Error() = %q, want it to name the server's floor", msg)
	}
}

func TestHandshakeRefusesWhenTheServersOwnWireRevisionIsNewerThanThisClient(t *testing.T) {
	// wire_revision ahead of this client, with a floor this client still
	// clears: this client cannot safely decode a shape newer than its own.
	future := ClientWireRevision + 5
	c, _ := newTestClient(t, Options{}, nil,
		serveContext(contextBodyWithWireRevision("v0", "proj-1", future, 0, "issues.list")))
	_, err := c.Handshake(ctx(t))
	if !errors.Is(err, ErrWireRevisionSkew) {
		t.Fatalf("err = %v, want ErrWireRevisionSkew", err)
	}
	var skew *WireRevisionSkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err is %T, want *WireRevisionSkewError", err)
	}
	if skew.ServerWireRevision != future {
		t.Errorf("ServerWireRevision = %d, want %d", skew.ServerWireRevision, future)
	}
}

func TestHandshakeWireRevisionGateRunsBeforeTheIdentityGate(t *testing.T) {
	// A server whose wire shape this client cannot speak is not a server worth
	// reporting a wrong-project diagnosis against either: the skew error must
	// win even when the project id ALSO disagrees.
	c, _ := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil,
		serveContext(contextBodyWithWireRevision("v0", "proj-2", ClientWireRevision, ClientWireRevision+1, "issues.list")))
	_, err := c.Handshake(ctx(t))
	if !errors.Is(err, ErrWireRevisionSkew) {
		t.Fatalf("err = %v, want ErrWireRevisionSkew (not a project-mismatch)", err)
	}
	if errors.Is(err, ErrProjectMismatch) {
		t.Fatal("err also satisfies ErrProjectMismatch; the wire-revision gate must win outright, not merely run first")
	}
}

func TestEveryRequestStampsTheClientsDeclaredWireRevision(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, serveContext(contextBodyWithWireRevision("v0", "proj-1", ClientWireRevision, 0, "issues.list")))
	if _, err := c.Handshake(ctx(t)); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	got := rec.at(t, 0).header.Get(WireRevisionHeader)
	if got != strconv.Itoa(ClientWireRevision) {
		t.Errorf("%s header = %q, want %q", WireRevisionHeader, got, strconv.Itoa(ClientWireRevision))
	}
}
