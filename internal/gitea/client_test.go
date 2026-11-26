package gitea

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://codeberg.org", "https://codeberg.org"},
		{"https://codeberg.org/", "https://codeberg.org"},
		{"https://codeberg.org/api/v1", "https://codeberg.org"},
		{"https://codeberg.org/api/v1/", "https://codeberg.org"},
		{"https://git.example.com/subpath/api/v1/", "https://git.example.com/subpath"},
		{"  https://forgejo.example/API/V1/  ", "https://forgejo.example"},
	}
	for _, tt := range tests {
		if got := NormalizeBaseURL(tt.in); got != tt.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateURLScheme(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://codeberg.org", false},
		{"https://git.example.com:8443/gitea", false},
		{"http://localhost:3000", false},
		{"http://127.0.0.1:3000", false},
		{"http://[::1]:3000", false},
		{"http://LOCALHOST/api/v1", false},
		{"http://localhost.attacker.tld", true},
		{"http://127.0.0.1.evil.com", true},
		{"http://gitea.example.com", true},
		{"ftp://gitea.example.com", true},
		{"", true},
		{"not-a-url", true},
	}
	for _, tt := range tests {
		err := ValidateURLScheme(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateURLScheme(%q) err = %v, wantErr = %v", tt.url, err, tt.wantErr)
		}
	}
}

func TestClientAuthHeaderAndPaths(t *testing.T) {
	var gotAuth, gotAccept, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":7,"username":"forgejo-user","login":"forgejo-user"}`))
	}))
	defer srv.Close()

	c := NewClient("secret-tok", srv.URL+"/api/v1/", "acme", "widgets")
	u, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("GetAuthenticatedUser: %v", err)
	}
	if gotAuth != "token secret-tok" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "token secret-tok")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if gotPath != "/api/v1/user" {
		t.Errorf("Path = %q, want /api/v1/user", gotPath)
	}
	if u.Handle() != "forgejo-user" {
		t.Errorf("Handle = %q, want forgejo-user", u.Handle())
	}
}

func TestClientWithTLSCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"login":"alice"}`))
	}))
	defer srv.Close()

	// Default client without custom CA must reject self-signed TLS server.
	untrusted := NewClient("tok", srv.URL, "acme", "widgets")
	if _, err := untrusted.GetAuthenticatedUser(context.Background()); err == nil {
		t.Fatal("expected TLS verification error without custom CA")
	}

	// Write server cert as PEM file and verify WithTLSCA trusts it.
	cert := srv.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	trusted, err := untrusted.WithTLSCA(caFile)
	if err != nil {
		t.Fatalf("WithTLSCA: %v", err)
	}
	u, err := trusted.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("GetAuthenticatedUser with custom CA: %v", err)
	}
	if u.Handle() != "alice" {
		t.Errorf("Handle = %q, want alice", u.Handle())
	}

	// Invalid PEM must return a clear error.
	badCA := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badCA, []byte("not-a-pem"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.WithTLSCA(badCA); err == nil {
		t.Fatal("expected error for invalid PEM CA file")
	}

	// Ensure unused crypto imports aren't needed; generate a fresh leaf check helper too.
	_ = generateTestCertPEM
}

func generateTestCertPEM(t *testing.T) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestFetchIssuesFiltersPRsAndPaginates(t *testing.T) {
	t.Run("link header pagination and PR filtering", func(t *testing.T) {
		var pageQueries []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/repos/acme/widgets/issues" {
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
			q := r.URL.Query()
			if q.Get("type") != "issues" {
				t.Errorf("type param = %q, want issues", q.Get("type"))
			}
			if q.Get("limit") != "50" {
				t.Errorf("limit param = %q, want 50", q.Get("limit"))
			}
			page := q.Get("page")
			pageQueries = append(pageQueries, page)
			if page == "1" {
				w.Header().Set("Link", fmt.Sprintf(`<http://%s/api/v1/repos/acme/widgets/issues?page=2>; rel="next"`, r.Host))
				_, _ = w.Write([]byte(`[
					{"id":10,"number":1,"title":"Real Issue 1","state":"open"},
					{"id":11,"number":2,"title":"Leaked PR","state":"open","pull_request":{"merged":false}}
				]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":12,"number":3,"title":"Real Issue 2","state":"closed"}]`))
		}))
		defer srv.Close()

		c := NewClient("tok", srv.URL, "acme", "widgets")
		issues, err := c.FetchIssues(context.Background(), "all")
		if err != nil {
			t.Fatalf("FetchIssues: %v", err)
		}
		if len(pageQueries) != 2 || pageQueries[0] != "1" || pageQueries[1] != "2" {
			t.Fatalf("pages queried = %v, want [1 2]", pageQueries)
		}
		if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 3 {
			t.Fatalf("issues = %+v, want numbers [1 3]", issues)
		}
	})

	t.Run("X-Total-Count pagination without Link header", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			w.Header().Set("X-Total-Count", "2")
			if page == "1" {
				_, _ = w.Write([]byte(`[{"id":1,"number":1,"title":"First","state":"open"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":2,"number":2,"title":"Second","state":"open"}]`))
		}))
		defer srv.Close()

		c := NewClient("tok", srv.URL, "acme", "widgets")
		issues, err := c.FetchIssues(context.Background(), "open")
		if err != nil {
			t.Fatalf("FetchIssues: %v", err)
		}
		if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
			t.Fatalf("issues = %+v, want 2 items", issues)
		}
	})
}

func TestRateLimitReturnsTrackerRateLimitedError(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "0")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, "acme", "widgets")
	c.Retry = RetryConfig{MaxRetries: 1, BaseDelay: time.Millisecond, MaxBackoff: 5 * time.Millisecond}

	_, err := c.GetAuthenticatedUser(context.Background())
	if err == nil {
		t.Fatal("expected RateLimitError")
	}
	var rl tracker.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("error %T (%v) does not implement tracker.RateLimitedError", err, err)
	}
	if !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Errorf("error %q missing server message", err.Error())
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2 (1 initial + 1 retry)", attempts.Load())
	}
}

func TestResolveLabelIDsAndReplaceIssueLabels(t *testing.T) {
	var createdNames []string
	var replacedIDs []int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			_, _ = w.Write([]byte(`[{"id":10,"name":"bug","color":"#d73a4a"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			var body struct {
				Name  string `json:"name"`
				Color string `json:"color"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			createdNames = append(createdNames, body.Name)
			if !strings.HasPrefix(body.Color, "#") || len(body.Color) != 7 {
				t.Errorf("label color = %q, want #RRGGBB", body.Color)
			}
			_, _ = fmt.Fprintf(w, `{"id":20,"name":%q,"color":%q}`, body.Name, body.Color)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/acme/widgets/issues/5/labels":
			var body struct {
				Labels []int64 `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			replacedIDs = body.Labels
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewClient("tok", srv.URL, "acme", "widgets")
	ids, err := c.ResolveLabelIDs(context.Background(), []string{"BUG", "priority::high"})
	if err != nil {
		t.Fatalf("ResolveLabelIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 20 {
		t.Fatalf("ids = %v, want [10 20]", ids)
	}
	if len(createdNames) != 1 || createdNames[0] != "priority::high" {
		t.Fatalf("createdNames = %v, want [priority::high]", createdNames)
	}

	if err := c.ReplaceIssueLabels(context.Background(), 5, ids); err != nil {
		t.Fatalf("ReplaceIssueLabels: %v", err)
	}
	if len(replacedIDs) != 2 || replacedIDs[0] != 10 || replacedIDs[1] != 20 {
		t.Fatalf("replacedIDs = %v, want [10 20]", replacedIDs)
	}
}
