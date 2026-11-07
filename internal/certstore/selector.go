// Package certstore selects a TLS client certificate from the Windows
// certificate store and signs with its private key through Windows, so a key
// enrolled as non-exportable (software, TPM or smart card) can authenticate a
// connection without ever leaving its key storage provider.
//
// The selection rules are platform independent and live in this file and
// rank.go; only enumeration, key acquisition and signing are Windows specific.
// On other platforms Configure returns ErrUnsupported.
package certstore

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned by Configure on platforms without a Windows
// certificate store.
var ErrUnsupported = errors.New("certstore: the Windows certificate store is only available on Windows")

// Location is a certificate store location.
type Location int

const (
	CurrentUser Location = iota + 1
	LocalMachine
)

func (l Location) String() string {
	switch l {
	case CurrentUser:
		return "CurrentUser"
	case LocalMachine:
		return "LocalMachine"
	}
	return fmt.Sprintf("Location(%d)", int(l))
}

// StorePath names one system certificate store, such as CurrentUser\My.
type StorePath struct {
	Location Location
	Name     string
}

func (p StorePath) String() string { return p.Location.String() + `\` + p.Name }

// DefaultSearchOrder is searched when a Selector names no store: the user's
// personal store first, then the machine's.
var DefaultSearchOrder = []StorePath{
	{Location: CurrentUser, Name: "My"},
	{Location: LocalMachine, Name: "My"},
}

// ParseStorePath parses `<Location>\<StoreName>`. The PowerShell drive prefix
// `Cert:\` is accepted, `/` may separate the parts, and the location is
// matched case-insensitively.
func ParseStorePath(s string) (StorePath, error) {
	orig := s
	s = strings.ReplaceAll(strings.TrimSpace(s), "/", `\`)
	if len(s) >= 5 && strings.EqualFold(s[:5], `cert:`) {
		s = strings.TrimPrefix(s[5:], `\`)
	}
	loc, name, ok := strings.Cut(s, `\`)
	if !ok || name == "" || strings.Contains(name, `\`) {
		return StorePath{}, fmt.Errorf("certstore: store %q: want <Location>\\<StoreName>, for example CurrentUser\\My or LocalMachine\\My", orig)
	}
	switch {
	case strings.EqualFold(loc, "CurrentUser"):
		return StorePath{Location: CurrentUser, Name: name}, nil
	case strings.EqualFold(loc, "LocalMachine"):
		return StorePath{Location: LocalMachine, Name: name}, nil
	}
	return StorePath{}, fmt.Errorf("certstore: store %q: location must be CurrentUser or LocalMachine", orig)
}

// Selector identifies the client certificate to use. Exactly one of Subject
// and Thumbprint is set. Store is optional; empty means DefaultSearchOrder.
type Selector struct {
	// Store is a store path accepted by ParseStorePath.
	Store string
	// Subject is a case-insensitive substring of the certificate subject as
	// Windows displays it, the string PowerShell prints for
	// (Get-Item Cert:\CurrentUser\My\<thumbprint>).Subject.
	Subject string
	// Thumbprint is the certificate's SHA-1 thumbprint in hex. Spaces and
	// colons are ignored.
	Thumbprint string
}

// Validate checks the selector's shape without touching any store.
func (s Selector) Validate() error {
	hasSubject := strings.TrimSpace(s.Subject) != ""
	hasThumb := strings.TrimSpace(s.Thumbprint) != ""
	switch {
	case hasSubject && hasThumb:
		return errors.New("certstore: set a subject or a thumbprint, not both")
	case !hasSubject && !hasThumb:
		return errors.New("certstore: a subject or a thumbprint is required")
	}
	if hasThumb {
		if _, err := normalizeThumbprint(s.Thumbprint); err != nil {
			return err
		}
	}
	_, err := s.searchOrder()
	return err
}

func (s Selector) searchOrder() ([]StorePath, error) {
	if strings.TrimSpace(s.Store) == "" {
		return DefaultSearchOrder, nil
	}
	p, err := ParseStorePath(s.Store)
	if err != nil {
		return nil, err
	}
	return []StorePath{p}, nil
}

func (s Selector) String() string {
	var b strings.Builder
	if strings.TrimSpace(s.Thumbprint) != "" {
		fmt.Fprintf(&b, "thumbprint %s", strings.TrimSpace(s.Thumbprint))
	} else {
		fmt.Fprintf(&b, "subject containing %q", strings.TrimSpace(s.Subject))
	}
	return b.String()
}

func normalizeThumbprint(t string) (string, error) {
	clean := strings.NewReplacer(" ", "", ":", "", "\u200e", "").Replace(strings.TrimSpace(t))
	raw, err := hex.DecodeString(clean)
	if err != nil || len(raw) != 20 {
		return "", fmt.Errorf("certstore: thumbprint %q is not a 40-digit hex SHA-1 thumbprint", t)
	}
	return strings.ToUpper(clean), nil
}
