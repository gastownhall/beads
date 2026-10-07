// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/handshake_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// contextBody is a ContextResponse carrying whatever capability list the case
// needs. The other members are what a real single-workspace server publishes.
func contextBody(apiVersion, projectID string, capabilities ...string) string {
	quoted := make([]string, 0, len(capabilities))
	for _, token := range capabilities {
		quoted = append(quoted, `"`+token+`"`)
	}
	return `{"api_version":"` + apiVersion + `","backend":"dolt","bd_version":"1.1.0",` +
		`"beads_dir":"/srv/repo/.beads","capabilities":[` + strings.Join(quoted, ",") + `],` +
		`"database":"beads","dolt_mode":"embedded","project_id":"` + projectID + `",` +
		`"repo_root":"/srv/repo","schema_version":7}`
}

func serveContext(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathContext {
			problemJSON(w, http.StatusNotFound, `{"status":404,"code":"not_found"}`)
			return
		}
		_, _ = io.WriteString(w, body)
	}
}

func TestGetContextDecodesTheWholeSnapshot(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", "issues.list", "issues.get")))
	got, err := c.GetContext(ctx(t))
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}
	if got.ApiVersion != "v0" || got.ProjectId != "proj-1" || got.BdVersion != "1.1.0" {
		t.Errorf("decoded %+v", got)
	}
	if got.Database != "beads" || deref(got.RepoRoot) != "/srv/repo" || got.SchemaVersion != 7 {
		t.Errorf("diagnostic members did not decode: %+v", got)
	}
	if len(got.Capabilities) != 2 {
		t.Errorf("capabilities = %v", got.Capabilities)
	}
}

func TestTheHandshakeIsFetchedOncePerClient(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", "issues.list")))
	for range 3 {
		if _, err := c.Handshake(ctx(t)); err != nil {
			t.Fatalf("Handshake: %v", err)
		}
	}
	if rec.count() != 1 {
		t.Errorf("made %d context fetches, want 1", rec.count())
	}
}

func TestConcurrentHandshakesStillFetchOnce(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", "issues.list")))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Handshake(ctx(t)); err != nil {
				t.Errorf("Handshake: %v", err)
			}
		}()
	}
	wg.Wait()
	if rec.count() != 1 {
		t.Errorf("made %d context fetches, want 1", rec.count())
	}
}

func TestAFailedHandshakeIsNotCached(t *testing.T) {
	// A server that was down when the first post-baseline command ran must not
	// poison the rest of the process.
	var attempts int
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			problemJSON(w, 503, `{"status":503,"code":"db_unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, contextBody("v0", "proj-1", "issues.list"))
	})
	if _, err := c.Handshake(ctx(t)); !errors.Is(err, ErrDBUnavailable) {
		t.Fatalf("err = %v, want ErrDBUnavailable", err)
	}
	snap, err := c.Handshake(ctx(t))
	if err != nil {
		t.Fatalf("second Handshake: %v", err)
	}
	if !snap.Has("issues.list") {
		t.Error("the recovered handshake carries no capabilities")
	}
}

func TestTheAPIVersionIsGated(t *testing.T) {
	// The wire is versioned by path, so a server on another major is a server
	// this client cannot address at all — not one it can degrade against.
	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v1", "proj-1", "issues.list")))
	_, err := c.Handshake(ctx(t))
	if !errors.Is(err, ErrAPIVersion) {
		t.Fatalf("err = %v, want ErrAPIVersion", err)
	}
	// Neither version alone tells an operator which side to move.
	msg := err.Error()
	if !strings.Contains(msg, `"v1"`) || !strings.Contains(msg, `"v0"`) {
		t.Errorf("Error() = %q, want both versions named", msg)
	}
}

func TestWorkspaceIdentityIsCheckedAtTheHandshake(t *testing.T) {
	t.Run("a match passes", func(t *testing.T) {
		c, _ := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil,
			serveContext(contextBody("v0", "proj-1", "issues.list")))
		if _, err := c.Handshake(ctx(t)); err != nil {
			t.Fatalf("Handshake: %v", err)
		}
	})

	t.Run("no recorded identity skips the check", func(t *testing.T) {
		c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "somebody-else", "issues.list")))
		if _, err := c.Handshake(ctx(t)); err != nil {
			t.Fatalf("Handshake: %v", err)
		}
	})

	t.Run("a mismatch names which wrong server answered", func(t *testing.T) {
		c, _ := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil,
			serveContext(contextBody("v0", "proj-2", "issues.list")))
		_, err := c.Handshake(ctx(t))
		if !errors.Is(err, ErrProjectMismatch) {
			t.Fatalf("err = %v, want ErrProjectMismatch", err)
		}
		// "The ids differ" does not say WHICH wrong server answered, and on a
		// shared host that is the whole question.
		msg := err.Error()
		for _, want := range []string{"proj-1", "proj-2", "beads", "/srv/repo", "bd connect"} {
			if !strings.Contains(msg, want) {
				t.Errorf("Error() = %q, want %q in it", msg, want)
			}
		}
	})
}

func TestBaselineOperationsPreflightWithoutDialing(t *testing.T) {
	// The five baseline operations exist in every v0 release, so the extra
	// round trip on the hot work-distribution path buys nothing. claimIssue is
	// first-slice too but is no longer exempt — it is a write and forces the
	// identity handshake (ga-b8ddd.11) — so it is not in this set.
	c, rec := newTestClient(t, Options{}, nil, func(http.ResponseWriter, *http.Request) {
		t.Error("a baseline pre-flight dialed the server")
	})
	for _, op := range []string{OpHealth, OpGetContext, OpListReadyWork, OpListIssues, OpGetIssue} {
		if !IsBaseline(op) {
			t.Errorf("%s is not classified baseline", op)
		}
		if err := c.Preflight(ctx(t), op); err != nil {
			t.Errorf("Preflight(%s): %v", op, err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("made %d requests, want 0", rec.count())
	}
}

func TestPostBaselineOperationsConsultTheCapabilityListBeforeDialing(t *testing.T) {
	// An unrouted path on an older server answers a bare 404 that is
	// indistinguishable from an entity's not_found, so the skew signal would
	// arrive spelled as "no such issue". The list is the only way to tell.
	all := make([]string, 0, len(opCapability))
	for op := range opCapability {
		if !IsBaseline(op) {
			all = append(all, op)
		}
	}
	served := make([]string, 0, len(all))
	for _, op := range all {
		token, _ := CapabilityFor(op)
		served = append(served, token)
	}

	c, rec := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", served...)))
	for _, op := range all {
		if err := c.Preflight(ctx(t), op); err != nil {
			t.Errorf("Preflight(%s): %v", op, err)
		}
	}
	if rec.count() != 1 {
		t.Errorf("made %d context fetches for %d operations, want 1", rec.count(), len(all))
	}
}

func TestAnAbsentCapabilityRefusesWithWhatTheTaxonomyNeeds(t *testing.T) {
	// Taxonomy case 2: a mapped command against an older server. Everything the
	// rendered text names travels on the typed error, so the render layer parses
	// nothing.
	c, _ := newTestClient(t, Options{}, nil,
		serveContext(contextBody("v0", "proj-1", "issues.list", "issues.get", "ready.list")))

	err := c.Preflight(ctx(t), OpAddDependencies)
	if !errors.Is(err, ErrCapabilityAbsent) {
		t.Fatalf("err = %v, want ErrCapabilityAbsent", err)
	}
	var absent *CapabilityError
	if !errors.As(err, &absent) {
		t.Fatalf("err is %T, want *CapabilityError", err)
	}
	if absent.Capability != "dependencies.add" || absent.Op != OpAddDependencies {
		t.Errorf("refusal = %+v", absent)
	}
	if absent.BdVersion != "1.1.0" {
		t.Errorf("BdVersion = %q", absent.BdVersion)
	}
	want := []string{"issues.get", "issues.list", "ready.list"}
	if len(absent.Capabilities) != len(want) {
		t.Fatalf("Capabilities = %v, want %v", absent.Capabilities, want)
	}
	for i, token := range want {
		// Sorted, so the refusal text is stable enough to byte-pin upstream.
		if absent.Capabilities[i] != token {
			t.Errorf("Capabilities = %v, want %v", absent.Capabilities, want)
		}
	}
}

func TestPreflightRefusesAnOperationThisClientDoesNotKnow(t *testing.T) {
	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1")))
	if err := c.Preflight(ctx(t), "teleportIssue"); err == nil {
		t.Error("an operation off the map pre-flighted clean")
	}
	if _, known := CapabilityFor("teleportIssue"); known {
		t.Error("CapabilityFor reported an unknown operation as known")
	}
}

func TestHealthIsTheLivenessProbe(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathHealth {
			t.Errorf("health dialed %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	if err := c.Health(ctx(t)); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if rec.count() != 1 {
		t.Errorf("made %d requests, want 1", rec.count())
	}
}
