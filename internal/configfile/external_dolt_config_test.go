package configfile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/certstore"
)

func TestExternalDoltConfig_Validate_CertStore(t *testing.T) {
	certPath, keyPath := writeSelfSignedPair(t)
	base := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true}

	// Each case must fail for its own reason on every platform, ahead of the
	// check that refuses a selector off Windows, so Linux CI covers them all.
	rejected := []struct {
		name string
		cfg  ExternalDoltConfig
		want string
	}{
		{"without TLSRequired", ExternalDoltConfig{Host: "db", Port: 3306, TLSCertSubject: "CN=alice"}, "selector set without TLSRequired"},
		{"combined with PEM", ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertSubject: "CN=alice", TLSCert: certPath, TLSKey: keyPath}, "cannot be combined with TLSCert/TLSKey"},
		{"subject and thumbprint", ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertSubject: "CN=alice", TLSCertThumbprint: strings.Repeat("ab", 20)}, "not both"},
		{"store without selector", ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertStore: `LocalMachine\My`}, "a subject or a thumbprint is required"},
		{"unknown store location", ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertStore: `Machine\My`, TLSCertSubject: "CN=alice"}, "location must be CurrentUser or LocalMachine"},
		{"short thumbprint", ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertThumbprint: "abcd"}, "40-digit"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate(%+v) = %v, want an error containing %q", tc.cfg, err, tc.want)
			}
		})
	}

	accepted := []ExternalDoltConfig{
		{TLSCertSubject: "CN=alice"},
		{TLSCertStore: `Cert:\LocalMachine\My`, TLSCertSubject: "alice@example.com"},
		{TLSCertThumbprint: "ab cd ef 01 23 45 67 89 ab cd ef 01 23 45 67 89 ab cd ef 01"},
	}
	for _, sel := range accepted {
		c := base
		c.TLSCertStore, c.TLSCertSubject, c.TLSCertThumbprint = sel.TLSCertStore, sel.TLSCertSubject, sel.TLSCertThumbprint
		err := c.Validate()
		if runtime.GOOS == "windows" {
			if err != nil {
				t.Errorf("Validate(%+v) = %v, want nil", c, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), runtime.GOOS) {
			t.Errorf("Validate(%+v) = %v, want an error naming %s", c, err, runtime.GOOS)
		}
	}
}

func TestExternalDoltConfig_CertStoreJSONKeys(t *testing.T) {
	raw, err := json.Marshal(ExternalDoltConfig{TLSCertStore: `LocalMachine\My`, TLSCertSubject: "CN=alice", TLSCertThumbprint: "AB"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"tls_cert_store":`, `"tls_cert_subject":`, `"tls_cert_thumbprint":`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("%s missing from %s", key, raw)
		}
	}
	raw, err = json.Marshal(ExternalDoltConfig{Host: "db", Port: 3306})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tls_cert_s") {
		t.Errorf("unset selector fields serialized: %s", raw)
	}
}

func TestExternalDoltConfig_TLSClientConfig_CertStore(t *testing.T) {
	c := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCertSubject: "CN=bd-test-no-such-certificate-7f3a9c"}
	cfg, closer, err := c.TLSClientConfig()
	if err == nil {
		_ = closer.Close()
		t.Fatalf("TLSClientConfig = %+v, want an error for a subject no certificate has", cfg)
	}
	if cfg != nil || closer != nil {
		t.Fatalf("TLSClientConfig returned a config or closer with error %v", err)
	}
	if runtime.GOOS != "windows" {
		if !errors.Is(err, certstore.ErrUnsupported) {
			t.Fatalf("err = %v, want certstore.ErrUnsupported", err)
		}
		return
	}
	var none *certstore.NoEligibleError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want *certstore.NoEligibleError", err)
	}
}

func TestExternalDoltConfig_ResolvedUser(t *testing.T) {
	t.Run("empty falls back to default", func(t *testing.T) {
		if got := (ExternalDoltConfig{}).ResolvedUser(); got != ExternalDoltConfigDefaultUser {
			t.Errorf("got %q, want %q", got, ExternalDoltConfigDefaultUser)
		}
	})

	t.Run("explicit user passes through", func(t *testing.T) {
		if got := (ExternalDoltConfig{User: "beads"}).ResolvedUser(); got != "beads" {
			t.Errorf("got %q, want %q", got, "beads")
		}
	})
}

func TestExternalDoltConfig_Validate_TLS(t *testing.T) {
	certPath, keyPath := writeSelfSignedPair(t)

	t.Run("ca cert must be absolute", func(t *testing.T) {
		err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: "relative/ca.pem"}.Validate()
		if err == nil {
			t.Fatal("expected error for relative TLSCACert")
		}
	})

	t.Run("absolute ca cert accepted", func(t *testing.T) {
		if err := (ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: certPath}).Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("socket with tls needs server name", func(t *testing.T) {
		err := ExternalDoltConfig{Socket: "/var/run/dolt.sock", TLSRequired: true}.Validate()
		if err == nil {
			t.Fatal("expected error for socket+tls without server name")
		}
	})

	t.Run("socket with tls and server name ok", func(t *testing.T) {
		if err := (ExternalDoltConfig{Socket: "/var/run/dolt.sock", TLSRequired: true, TLSServerName: "db"}).Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("socket with tls and skip verify ok", func(t *testing.T) {
		if err := (ExternalDoltConfig{Socket: "/var/run/dolt.sock", TLSRequired: true, TLSSkipVerify: true}).Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("tls material without tls required is rejected", func(t *testing.T) {
		cases := []ExternalDoltConfig{
			{Host: "db", Port: 3306, TLSCACert: certPath},
			{Host: "db", Port: 3306, TLSCert: certPath, TLSKey: keyPath},
			{Host: "db", Port: 3306, TLSServerName: "db"},
			{Host: "db", Port: 3306, TLSSkipVerify: true},
		}
		for _, c := range cases {
			if err := c.Validate(); err == nil {
				t.Fatalf("expected error for TLS material without TLSRequired: %+v", c)
			}
		}
	})
}

func TestExternalDoltConfig_TLSClientConfig(t *testing.T) {
	certPath, keyPath := writeSelfSignedPair(t)

	t.Run("not required returns nil", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db", Port: 3306}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got %+v", cfg)
		}
	})

	t.Run("server name defaults to host", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db.example.com", Port: 3306, TLSRequired: true}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ServerName != "db.example.com" {
			t.Fatalf("ServerName = %q, want db.example.com", cfg.ServerName)
		}
		if cfg.MinVersion == 0 {
			t.Fatal("MinVersion not set")
		}
	})

	t.Run("server name defaults to an IPv6 host in either spelling, bare", func(t *testing.T) {
		for _, host := range []string{"2001:db8::1", "[2001:db8::1]"} {
			cfg, _, err := ExternalDoltConfig{Host: host, Port: 3306, TLSRequired: true}.TLSClientConfig()
			if err != nil {
				t.Fatalf("host %q: unexpected error: %v", host, err)
			}
			if cfg.ServerName != "2001:db8::1" {
				t.Errorf("host %q: ServerName = %q, want 2001:db8::1", host, cfg.ServerName)
			}
		}
	})

	t.Run("server name override wins", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "127.0.0.1", Port: 3306, TLSRequired: true, TLSServerName: "real.host"}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ServerName != "real.host" {
			t.Fatalf("ServerName = %q, want real.host", cfg.ServerName)
		}
	})

	t.Run("skip verify sets insecure and clears server name", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSSkipVerify: true, TLSServerName: "db"}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.InsecureSkipVerify {
			t.Fatal("InsecureSkipVerify not set")
		}
		if cfg.ServerName != "" {
			t.Fatalf("ServerName = %q, want empty under skip-verify", cfg.ServerName)
		}
	})

	t.Run("ca cert loaded into root pool", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: certPath}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RootCAs == nil {
			t.Fatal("RootCAs not set from CA cert")
		}
	})

	t.Run("empty ca cert uses system roots", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RootCAs != nil {
			t.Fatal("RootCAs should be nil to use system roots")
		}
	})

	t.Run("unparseable ca cert errors", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad.pem")
		if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: bad}.TLSClientConfig()
		if err == nil {
			t.Fatal("expected error for unparseable CA cert")
		}
	})

	t.Run("client cert loaded for mtls", func(t *testing.T) {
		cfg, _, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCert: certPath, TLSKey: keyPath}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Certificates) != 1 {
			t.Fatalf("Certificates len = %d, want 1", len(cfg.Certificates))
		}
	})
}

func writeSelfSignedPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
