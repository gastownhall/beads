package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
)

// fakeCredential is a realistic sync remote: a token in the userinfo of an
// HTTPS URL, which is how every token-authenticated git remote is written.
const (
	fakeToken       = "ghs_NOTAREALTOKENabcdefghijklmnop"
	fakeSyncRemote  = "https://x-access-token:" + fakeToken + "@github.com/example/private.git"
	fakeDoltAddress = "10.4.2.9"
)

// populatedContextInfo fills EVERY field of the snapshot with a distinct
// sentinel. A field that leaks shows up as its own sentinel in the body, so the
// assertions below name the leak instead of just failing a count.
func populatedContextInfo() domain.ContextInfo {
	return domain.ContextInfo{
		BeadsDir:     "/host/workspace/.beads",
		RepoRoot:     "/host/workspace",
		CWDRepoRoot:  "/host/elsewhere",
		IsRedirected: true,
		IsWorktree:   true,
		Backend:      "dolt",
		DoltMode:     "proxied-server",
		ServerHost:   fakeDoltAddress,
		ServerPort:   3307,
		ProxiedDir:   "/host/workspace/.beads/dolt",
		Database:     "beads",
		DataDir:      "/host/workspace/.beads/embeddeddolt",
		ProjectID:    "proj-1",
		SyncRemote:   fakeSyncRemote,
		Role:         "maintainer",
		BdVersion:    "9.9.9",
	}
}

// TestContextHandlerServesOnlyTheAllowlist is the enforcement half of the
// context field allowlist: TestContextResponseAllowlist pins what the document
// and the generated struct agree to, this pins what the handler actually
// writes.
//
// It matters because the source of this response is the server's own
// configuration — exactly the kind of struct that grows a member nobody meant
// to publish. A future field on domain.ContextInfo, or a refactor that
// "simplifies" the projection into marshalling the struct, fails here.
func TestContextHandlerServesOnlyTheAllowlist(t *testing.T) {
	ts := newTestServer(t, Config{
		Workspace:     populatedContextInfo(),
		SchemaVersion: 1,
	})

	resp := ts.get(t, "/v0/beads/context")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}

	want := map[string]bool{}
	for _, name := range contextResponseAllowlist {
		want[name] = true
	}
	got := map[string]bool{}
	for name := range body {
		got[name] = true
	}
	if extra := diff(got, want); len(extra) > 0 {
		t.Errorf("response carries fields outside the allowlist: %v\n"+
			"every member of this body is a permanent, deliberate disclosure", extra)
	}
	if missing := diff(want, got); len(missing) > 0 {
		t.Errorf("allowlisted fields absent from the response: %v", missing)
	}

	// The credential check is separate from the key check on purpose: a sync
	// remote could also arrive embedded in some other field's value, and the
	// key set alone would not notice.
	if strings.Contains(string(raw), fakeToken) {
		t.Errorf("the sync remote's credential appears in the response body:\n%s", raw)
	}
	if strings.Contains(string(raw), fakeSyncRemote) {
		t.Errorf("the sync remote URL appears in the response body:\n%s", raw)
	}
	// Advertising the database endpoint invites clients to bypass this API and
	// dial a server whose trust model is "root, empty password, loopback".
	if strings.Contains(string(raw), fakeDoltAddress) || strings.Contains(string(raw), "3307") {
		t.Errorf("the database bind endpoint appears in the response body:\n%s", raw)
	}

	if body["api_version"] != APIVersion {
		t.Errorf("api_version = %v, want %q", body["api_version"], APIVersion)
	}
	if body["bd_version"] != "9.9.9" {
		t.Errorf("bd_version = %v", body["bd_version"])
	}
	if body["beads_dir"] != "/host/workspace/.beads" {
		t.Errorf("beads_dir = %v", body["beads_dir"])
	}
	// The document types capabilities as an array; a client must never have to
	// tell null from empty to learn that nothing is implemented yet.
	if _, ok := body["capabilities"].([]any); !ok {
		t.Errorf("capabilities = %#v, want a JSON array", body["capabilities"])
	}
}
