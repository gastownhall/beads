// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/credential_source_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// sourcedToken is a CredentialProvider that can name the rung its credential came
// from — the optional Source() half the wire client consults on a 401 (design
// D7). staticToken deliberately does not implement it, so the two together cover
// both branches of credentialSource.
type sourcedToken struct {
	token    string
	source   string
	retry    bool
	refreshN int
}

func (s *sourcedToken) Authorize(_ context.Context, req *http.Request) error {
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return nil
}

func (s *sourcedToken) Refresh(context.Context) (bool, error) {
	s.refreshN++
	return s.retry, nil
}

func (s *sourcedToken) Source() string { return s.source }

func TestARefusedCredentialNamesItsSource(t *testing.T) {
	// The 401 the operator sees must say WHICH source held the credential the
	// server refused, so they know what to rotate or fix rather than guessing
	// across three rungs.
	creds := &sourcedToken{token: "wrong", source: "BEADS_HTTP_TOKEN", retry: false}
	c, _ := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if !strings.Contains(err.Error(), "credential from BEADS_HTTP_TOKEN") {
		t.Errorf("401 does not name the credential source:\n%s", err)
	}
	// The provenance slug is safe; the credential itself must never appear.
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("the credential leaked into the 401 text: %v", err)
	}
}

func TestThe401AfterTheRotationRetryStillNamesTheSource(t *testing.T) {
	// The source has to survive the one rotation retry: a client that rolled its
	// credential and still 401s wants the rung named on the FINAL answer, not
	// dropped because a retry happened.
	creds := &sourcedToken{token: "rotated", source: "the credentials file [10.0.0.5:8080]", retry: true}
	c, rec := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want the one retry then the refusal", rec.count())
	}
	if !strings.Contains(err.Error(), "credential from the credentials file [10.0.0.5:8080]") {
		t.Errorf("the post-retry 401 does not name the source:\n%s", err)
	}
}

func TestA401FromAProviderWithNoSourceIsUnadorned(t *testing.T) {
	// A provider that cannot report a source — the no-auth posture, or a
	// third-party provider — must not force a clause. The 401 reads as it did
	// before, and nothing panics on the missing capability.
	creds := &staticToken{tokens: []string{"tok"}, retry: false}
	c, _ := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		unauthenticated(w)
	})

	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if strings.Contains(err.Error(), "credential from") {
		t.Errorf("a sourceless provider still produced a source clause: %v", err)
	}
}
