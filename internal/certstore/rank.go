package certstore

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha1" // #nosec G505 -- the Windows thumbprint is defined as SHA-1; it identifies, it does not authenticate
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Candidate is one certificate read from a store, with the facts selection
// needs that x509 parsing alone does not provide.
type Candidate struct {
	Cert *x509.Certificate
	// Subject is the subject as Windows displays it; see Selector.Subject.
	Subject string
	// HasPrivateKey reports whether the store associates a private key with
	// the certificate. It is read from certificate properties without opening
	// the key.
	HasPrivateKey bool
}

// Thumbprint is the certificate's SHA-1 thumbprint in upper-case hex, the
// identifier Windows tools show.
func (c Candidate) Thumbprint() string {
	sum := sha1.Sum(c.Cert.Raw) // #nosec G401 -- identifier, see import
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Rejection records why a certificate that matched the selector was not used.
type Rejection struct {
	Store      StorePath
	Thumbprint string
	Subject    string
	Reason     string
}

// NoEligibleError reports that no searched store held an eligible
// certificate. It lists every certificate that matched the selector and why
// each was rejected.
type NoEligibleError struct {
	Selector Selector
	Searched []StorePath
	Rejected []Rejection
}

func (e *NoEligibleError) Error() string {
	var b strings.Builder
	stores := make([]string, len(e.Searched))
	for i, s := range e.Searched {
		stores[i] = s.String()
	}
	fmt.Fprintf(&b, "certstore: no eligible client certificate with %s in %s", e.Selector, strings.Join(stores, ", "))
	if len(e.Rejected) == 0 {
		b.WriteString(": no certificate matched")
		return b.String()
	}
	b.WriteString("; certificates that matched but were rejected:")
	for _, r := range e.Rejected {
		fmt.Fprintf(&b, "\n  %s %s (%s): %s", r.Store, r.Thumbprint, r.Subject, r.Reason)
	}
	return b.String()
}

// matches reports whether c is the certificate the selector names, before
// any eligibility rule is applied. thumb must already be normalized.
func (s Selector) matches(c Candidate, thumb string) bool {
	if thumb != "" {
		return c.Thumbprint() == thumb
	}
	return strings.Contains(strings.ToLower(c.Subject), strings.ToLower(strings.TrimSpace(s.Subject)))
}

// ineligibleReason returns why c cannot be used as a TLS client certificate
// at now, or "" when it can.
func ineligibleReason(c Candidate, now time.Time) string {
	cert := c.Cert
	switch {
	case now.Before(cert.NotBefore):
		return "not valid until " + cert.NotBefore.UTC().Format(time.RFC3339)
	case !now.Before(cert.NotAfter):
		return "expired " + cert.NotAfter.UTC().Format(time.RFC3339)
	case !hasClientAuthEKU(cert):
		return "extended key usage does not include client authentication (1.3.6.1.5.5.7.3.2)"
	case cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageDigitalSignature == 0:
		return "key usage does not include digital signature"
	case !supportedKey(cert):
		return fmt.Sprintf("unsupported public key type %T; only RSA and ECDSA keys can sign TLS handshakes", cert.PublicKey)
	case !c.HasPrivateKey:
		return "no private key is associated with the certificate in this store"
	}
	return ""
}

func hasClientAuthEKU(cert *x509.Certificate) bool {
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

func supportedKey(cert *x509.Certificate) bool {
	switch cert.PublicKey.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return true
	}
	return false
}

// choose returns the most recently issued eligible certificate in cands that
// the selector names, and a rejection for every matching certificate it
// passed over as ineligible. accept, when non-nil, is an additional filter
// (for example the server's acceptable CAs); its rejections carry reason.
func (s Selector) choose(store StorePath, cands []Candidate, now time.Time, accept func(Candidate) (bool, string)) (Candidate, bool, []Rejection) {
	thumb := ""
	if strings.TrimSpace(s.Thumbprint) != "" {
		thumb, _ = normalizeThumbprint(s.Thumbprint)
	}
	var eligible []Candidate
	var rejected []Rejection
	for _, c := range cands {
		if !s.matches(c, thumb) {
			continue
		}
		reason := ineligibleReason(c, now)
		if reason == "" && accept != nil {
			if ok, why := accept(c); !ok {
				reason = why
			}
		}
		if reason != "" {
			rejected = append(rejected, Rejection{Store: store, Thumbprint: c.Thumbprint(), Subject: c.Subject, Reason: reason})
			continue
		}
		eligible = append(eligible, c)
	}
	if len(eligible) == 0 {
		return Candidate{}, false, rejected
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i].Cert, eligible[j].Cert
		if !a.NotBefore.Equal(b.NotBefore) {
			return a.NotBefore.After(b.NotBefore)
		}
		if !a.NotAfter.Equal(b.NotAfter) {
			return a.NotAfter.After(b.NotAfter)
		}
		return eligible[i].Thumbprint() < eligible[j].Thumbprint()
	})
	return eligible[0], true, rejected
}

// search applies choose to each store in order and returns the first store's
// winner. Stores are never ranked together: an eligible certificate in an
// earlier store wins over a newer one in a later store. open returns the
// candidates of one store; an error from it (other than a missing store)
// stops the search.
func (s Selector) search(open func(StorePath) ([]Candidate, error), now time.Time, accept func(Candidate) (bool, string)) (Candidate, StorePath, error) {
	order, err := s.searchOrder()
	if err != nil {
		return Candidate{}, StorePath{}, err
	}
	noneErr := &NoEligibleError{Selector: s}
	for _, store := range order {
		cands, err := open(store)
		if err != nil {
			return Candidate{}, StorePath{}, fmt.Errorf("certstore: reading %s: %w", store, err)
		}
		noneErr.Searched = append(noneErr.Searched, store)
		c, ok, rejected := s.choose(store, cands, now, accept)
		noneErr.Rejected = append(noneErr.Rejected, rejected...)
		if ok {
			return c, store, nil
		}
	}
	return Candidate{}, StorePath{}, noneErr
}
