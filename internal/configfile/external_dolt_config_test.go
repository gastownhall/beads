package configfile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		cfg, err := ExternalDoltConfig{Host: "db", Port: 3306}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Fatalf("expected nil config, got %+v", cfg)
		}
	})

	t.Run("server name defaults to host", func(t *testing.T) {
		cfg, err := ExternalDoltConfig{Host: "db.example.com", Port: 3306, TLSRequired: true}.TLSClientConfig()
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

	t.Run("server name override wins", func(t *testing.T) {
		cfg, err := ExternalDoltConfig{Host: "127.0.0.1", Port: 3306, TLSRequired: true, TLSServerName: "real.host"}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ServerName != "real.host" {
			t.Fatalf("ServerName = %q, want real.host", cfg.ServerName)
		}
	})

	t.Run("skip verify sets insecure and clears server name", func(t *testing.T) {
		cfg, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSSkipVerify: true, TLSServerName: "db"}.TLSClientConfig()
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
		cfg, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: certPath}.TLSClientConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RootCAs == nil {
			t.Fatal("RootCAs not set from CA cert")
		}
	})

	t.Run("empty ca cert uses system roots", func(t *testing.T) {
		cfg, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true}.TLSClientConfig()
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
		_, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCACert: bad}.TLSClientConfig()
		if err == nil {
			t.Fatal("expected error for unparseable CA cert")
		}
	})

	t.Run("client cert loaded for mtls", func(t *testing.T) {
		cfg, err := ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true, TLSCert: certPath, TLSKey: keyPath}.TLSClientConfig()
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
