package configfile

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
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
	TLSCert         string        `json:"tls_cert,omitempty"`
	TLSKey          string        `json:"tls_key,omitempty"`
	KeepAlivePeriod time.Duration `json:"keep_alive_period,omitempty"`
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

	if c.KeepAlivePeriod < 0 {
		return fmt.Errorf("ExternalDoltConfig: KeepAlivePeriod %s is negative", c.KeepAlivePeriod)
	}

	return nil
}
