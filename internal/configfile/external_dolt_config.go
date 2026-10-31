package configfile

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/steveyegge/beads/internal/certstore"
)

const (
	ExternalDoltConfigDefaultUser = "root"
	ExternalDoltPasswordEnvVar    = "BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD" // #nosec G101 -- env var name, not a credential
)

type ExternalDoltConfig struct {
	Host            string        `json:"host,omitempty"`
	Port            int           `json:"port,omitempty"`
	Socket          string        `json:"socket,omitempty"`
	User            string        `json:"user,omitempty"`
	TLSRequired     bool          `json:"tls_required,omitempty"`
	TLSCACert       string        `json:"tls_ca_cert,omitempty"`
	TLSCert         string        `json:"tls_cert,omitempty"`
	TLSKey          string        `json:"tls_key,omitempty"`
	TLSServerName   string        `json:"tls_server_name,omitempty"`
	TLSSkipVerify   bool          `json:"tls_skip_verify,omitempty"`
	KeepAlivePeriod time.Duration `json:"keep_alive_period,omitempty"`

	// TLSCertStore, TLSCertSubject and TLSCertThumbprint select the client
	// certificate from the Windows certificate store instead of PEM files;
	// see certstore.Selector.
	TLSCertStore      string `json:"tls_cert_store,omitempty"`
	TLSCertSubject    string `json:"tls_cert_subject,omitempty"`
	TLSCertThumbprint string `json:"tls_cert_thumbprint,omitempty"`
}

func (c ExternalDoltConfig) ResolvedUser() string {
	if c.User == "" {
		return ExternalDoltConfigDefaultUser
	}
	return c.User
}

func (c ExternalDoltConfig) Validate() error {
	hasHost := c.Host != ""
	hasPort := c.Port != 0
	hasSocket := c.Socket != ""

	switch {
	case hasSocket && (hasHost || hasPort):
		return errors.New("ExternalDoltConfig: set either Socket OR (Host, Port), not both")
	case !hasSocket && !hasHost && !hasPort:
		return errors.New("ExternalDoltConfig: must set Socket or (Host, Port)")
	case hasHost && !hasPort:
		return errors.New("ExternalDoltConfig: Host requires Port")
	case !hasHost && hasPort:
		return errors.New("ExternalDoltConfig: Port requires Host")
	}

	if hasHost && (c.Port < 1 || c.Port > 65535) {
		return fmt.Errorf("ExternalDoltConfig: Port %d out of range [1, 65535]", c.Port)
	}

	if hasSocket && !filepath.IsAbs(c.Socket) {
		return fmt.Errorf("ExternalDoltConfig: Socket %q is not absolute", c.Socket)
	}

	switch {
	case c.TLSCert != "" && c.TLSKey == "":
		return errors.New("ExternalDoltConfig: TLSCert set without TLSKey")
	case c.TLSCert == "" && c.TLSKey != "":
		return errors.New("ExternalDoltConfig: TLSKey set without TLSCert")
	}

	if c.TLSCert != "" && !filepath.IsAbs(c.TLSCert) {
		return fmt.Errorf("ExternalDoltConfig: TLSCert %q is not absolute", c.TLSCert)
	}
	if c.TLSKey != "" && !filepath.IsAbs(c.TLSKey) {
		return fmt.Errorf("ExternalDoltConfig: TLSKey %q is not absolute", c.TLSKey)
	}
	if c.TLSCACert != "" && !filepath.IsAbs(c.TLSCACert) {
		return fmt.Errorf("ExternalDoltConfig: TLSCACert %q is not absolute", c.TLSCACert)
	}

	if c.usesCertStore() {
		if c.TLSCert != "" {
			return errors.New("ExternalDoltConfig: a certificate store selector cannot be combined with TLSCert/TLSKey")
		}
		if err := c.certStoreSelector().Validate(); err != nil {
			return fmt.Errorf("ExternalDoltConfig: %w", err)
		}
	}

	if !c.TLSRequired {
		switch {
		case c.TLSCACert != "":
			return errors.New("ExternalDoltConfig: TLSCACert set without TLSRequired")
		case c.TLSCert != "" || c.TLSKey != "":
			return errors.New("ExternalDoltConfig: TLSCert/TLSKey set without TLSRequired")
		case c.usesCertStore():
			return errors.New("ExternalDoltConfig: certificate store selector set without TLSRequired")
		case c.TLSServerName != "":
			return errors.New("ExternalDoltConfig: TLSServerName set without TLSRequired")
		case c.TLSSkipVerify:
			return errors.New("ExternalDoltConfig: TLSSkipVerify set without TLSRequired")
		}
	}

	if c.usesCertStore() && runtime.GOOS != "windows" {
		return fmt.Errorf("ExternalDoltConfig: a certificate store selector needs the Windows certificate store, which %s does not have", runtime.GOOS)
	}

	if c.TLSRequired && hasSocket && c.TLSServerName == "" && !c.TLSSkipVerify {
		return errors.New("ExternalDoltConfig: TLSRequired over Socket needs TLSServerName or TLSSkipVerify")
	}

	if c.KeepAlivePeriod < 0 {
		return fmt.Errorf("ExternalDoltConfig: KeepAlivePeriod %s is negative", c.KeepAlivePeriod)
	}

	return nil
}

func (c ExternalDoltConfig) usesCertStore() bool {
	return c.TLSCertStore != "" || c.TLSCertSubject != "" || c.TLSCertThumbprint != ""
}

func (c ExternalDoltConfig) certStoreSelector() certstore.Selector {
	return certstore.Selector{Store: c.TLSCertStore, Subject: c.TLSCertSubject, Thumbprint: c.TLSCertThumbprint}
}

// TLSClientConfig builds the client TLS configuration. The returned closer,
// when not nil, releases the certificate store handles the configuration
// signs with; close it once no connection can still handshake with it.
func (c ExternalDoltConfig) TLSClientConfig() (*tls.Config, io.Closer, error) {
	if !c.TLSRequired {
		return nil, nil, nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if c.TLSSkipVerify {
		cfg.InsecureSkipVerify = true //nolint:gosec // G402: opt-in insecure transport via the TLSSkipVerify testing flag
	} else {
		// A host configured bracketed ("[2001:db8::1]") is named bare, the IP
		// it is: the form every other consumer of the host takes.
		name := c.TLSServerName
		if name == "" {
			name = UnbracketHost(c.Host)
		}
		cfg.ServerName = name
	}

	if c.TLSCACert != "" {
		pem, err := os.ReadFile(c.TLSCACert)
		if err != nil {
			return nil, nil, fmt.Errorf("ExternalDoltConfig: read TLSCACert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("ExternalDoltConfig: TLSCACert %q: no certificates parsed", c.TLSCACert)
		}
		cfg.RootCAs = pool
	}

	if c.TLSCert != "" {
		crt, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return nil, nil, fmt.Errorf("ExternalDoltConfig: load client cert/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{crt}
	}

	if c.usesCertStore() {
		closer, err := c.certStoreSelector().Configure(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("ExternalDoltConfig: %w", err)
		}
		return cfg, closer, nil
	}

	return cfg, nil, nil
}
