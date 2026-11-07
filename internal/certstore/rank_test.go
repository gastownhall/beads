package certstore

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type certSpec struct {
	cn        string
	notBefore time.Time
	notAfter  time.Time
	eku       []x509.ExtKeyUsage
	keyUsage  x509.KeyUsage
	ed25519   bool
	noKey     bool
	serial    int64
}

func newCandidate(t *testing.T, s certSpec) Candidate {
	t.Helper()
	if s.notBefore.IsZero() {
		s.notBefore = testNow.Add(-24 * time.Hour)
	}
	if s.notAfter.IsZero() {
		s.notAfter = testNow.Add(365 * 24 * time.Hour)
	}
	if s.eku == nil {
		s.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	if s.serial == 0 {
		s.serial = 1
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(s.serial),
		Subject:      pkix.Name{CommonName: s.cn, Organization: []string{"Contoso"}},
		NotBefore:    s.notBefore,
		NotAfter:     s.notAfter,
		ExtKeyUsage:  s.eku,
		KeyUsage:     s.keyUsage,
	}
	var pub, priv any
	if s.ed25519 {
		pk, sk, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = pk, sk
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = &k.PublicKey, k
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{Cert: cert, Subject: "CN=" + s.cn + ", O=Contoso", HasPrivateKey: !s.noKey}
}

func TestParseStorePath(t *testing.T) {
	cases := []struct {
		in   string
		want StorePath
		err  bool
	}{
		{in: `CurrentUser\My`, want: StorePath{CurrentUser, "My"}},
		{in: `localmachine\My`, want: StorePath{LocalMachine, "My"}},
		{in: `Cert:\CurrentUser\My`, want: StorePath{CurrentUser, "My"}},
		{in: `cert:/LocalMachine/My`, want: StorePath{LocalMachine, "My"}},
		{in: ` CurrentUser\Remote Desktop `, want: StorePath{CurrentUser, "Remote Desktop"}},
		{in: `My`, err: true},
		{in: `CurrentUser\`, err: true},
		{in: `CurrentUser\My\Extra`, err: true},
		{in: `Users\My`, err: true},
	}
	for _, c := range cases {
		got, err := ParseStorePath(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseStorePath(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseStorePath(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestSelectorValidate(t *testing.T) {
	thumb := "C26C3F9482E135CB053D5A0BDCA3E6891A801CDE"
	cases := []struct {
		name string
		sel  Selector
		ok   bool
	}{
		{"subject", Selector{Subject: "CN=Jane"}, true},
		{"thumbprint", Selector{Thumbprint: thumb}, true},
		{"thumbprint with spaces, colons and lower case", Selector{Thumbprint: "c2:6c 3f94 82e135cb053d5a0bdca3e6891a801cde"}, true},
		{"thumbprint copied from certmgr with a left-to-right mark", Selector{Thumbprint: "\u200e" + thumb}, true},
		{"subject and store", Selector{Subject: "Jane", Store: `LocalMachine\My`}, true},
		{"neither", Selector{}, false},
		{"both", Selector{Subject: "Jane", Thumbprint: thumb}, false},
		{"short thumbprint", Selector{Thumbprint: "C26C3F"}, false},
		{"bad store", Selector{Subject: "Jane", Store: "My"}, false},
	}
	for _, c := range cases {
		err := c.sel.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestChooseEligibility(t *testing.T) {
	store := StorePath{CurrentUser, "My"}
	cases := []struct {
		name   string
		spec   certSpec
		reason string
	}{
		{"eligible", certSpec{cn: "Jane"}, ""},
		{"not yet valid", certSpec{cn: "Jane", notBefore: testNow.Add(time.Hour)}, "not valid until"},
		{"expired", certSpec{cn: "Jane", notAfter: testNow.Add(-time.Hour)}, "expired"},
		{"expires exactly now", certSpec{cn: "Jane", notAfter: testNow}, "expired"},
		{"server auth only", certSpec{cn: "Jane", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, "client authentication"},
		{"no EKU extension", certSpec{cn: "Jane", eku: []x509.ExtKeyUsage{}}, "client authentication"},
		{"any EKU only", certSpec{cn: "Jane", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}, "client authentication"},
		{"key usage without digital signature", certSpec{cn: "Jane", keyUsage: x509.KeyUsageKeyEncipherment}, "digital signature"},
		{"key usage with digital signature", certSpec{cn: "Jane", keyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}, ""},
		{"ed25519 key", certSpec{cn: "Jane", ed25519: true}, "unsupported public key type"},
		{"no private key", certSpec{cn: "Jane", noKey: true}, "no private key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cand := newCandidate(t, c.spec)
			got, ok, rejected := Selector{Subject: "jane"}.choose(store, []Candidate{cand}, testNow, nil)
			if c.reason == "" {
				if !ok || got.Cert != cand.Cert || len(rejected) != 0 {
					t.Fatalf("want chosen, got ok=%v rejected=%v", ok, rejected)
				}
				return
			}
			if ok {
				t.Fatalf("want rejected for %q, got chosen", c.reason)
			}
			if len(rejected) != 1 || !strings.Contains(rejected[0].Reason, c.reason) {
				t.Fatalf("rejections = %+v, want one containing %q", rejected, c.reason)
			}
		})
	}
}

func TestChooseRanking(t *testing.T) {
	store := StorePath{CurrentUser, "My"}
	day := 24 * time.Hour
	older := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-300 * day), notAfter: testNow.Add(400 * day), serial: 1})
	newer := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-10 * day), notAfter: testNow.Add(355 * day), serial: 2})
	newestExpired := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-2 * day), notAfter: testNow.Add(-day), serial: 3})
	newestServerOnly := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, serial: 4})
	other := newCandidate(t, certSpec{cn: "John Roe", notBefore: testNow.Add(-time.Hour), serial: 5})

	got, ok, rejected := Selector{Subject: "CN=JANE DOE"}.choose(store, []Candidate{older, newestExpired, other, newer, newestServerOnly}, testNow, nil)
	if !ok || got.Cert != newer.Cert {
		t.Fatalf("chose %v (ok=%v), want the most recently issued eligible certificate", got.Cert.NotBefore, ok)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejections = %+v, want the expired and the server-auth-only certificates", rejected)
	}

	t.Run("same issue date prefers later expiry", func(t *testing.T) {
		a := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), notAfter: testNow.Add(30 * day), serial: 6})
		b := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), notAfter: testNow.Add(60 * day), serial: 7})
		got, _, _ := Selector{Subject: "Jane"}.choose(store, []Candidate{a, b}, testNow, nil)
		if got.Cert != b.Cert {
			t.Fatal("want the certificate that expires later")
		}
	})

	t.Run("full tie is broken by thumbprint, independent of order", func(t *testing.T) {
		a := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), notAfter: testNow.Add(30 * day), serial: 8})
		b := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), notAfter: testNow.Add(30 * day), serial: 9})
		first, _, _ := Selector{Subject: "Jane"}.choose(store, []Candidate{a, b}, testNow, nil)
		second, _, _ := Selector{Subject: "Jane"}.choose(store, []Candidate{b, a}, testNow, nil)
		if first.Cert != second.Cert {
			t.Fatal("tie-break depends on enumeration order")
		}
	})

	t.Run("thumbprint selects exactly one certificate and still applies eligibility", func(t *testing.T) {
		got, ok, _ := Selector{Thumbprint: older.Thumbprint()}.choose(store, []Candidate{older, newer}, testNow, nil)
		if !ok || got.Cert != older.Cert {
			t.Fatal("thumbprint did not select the pinned certificate")
		}
		_, ok, rejected := Selector{Thumbprint: strings.ToLower(newestExpired.Thumbprint())}.choose(store, []Candidate{newestExpired, newer}, testNow, nil)
		if ok || len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "expired") {
			t.Fatalf("pinned expired certificate: ok=%v rejected=%+v", ok, rejected)
		}
	})

	t.Run("accept filter rejects with its reason", func(t *testing.T) {
		deny := func(c Candidate) (bool, string) {
			return c.Cert != newer.Cert, "issuer is not acceptable to the server"
		}
		got, ok, rejected := Selector{Subject: "Jane Doe"}.choose(store, []Candidate{older, newer}, testNow, deny)
		if !ok || got.Cert != older.Cert {
			t.Fatal("want the older certificate once the newer one is filtered out")
		}
		if len(rejected) != 1 || rejected[0].Reason != "issuer is not acceptable to the server" {
			t.Fatalf("rejections = %+v", rejected)
		}
	})
}

func TestSearchOrder(t *testing.T) {
	day := 24 * time.Hour
	userOld := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-200 * day), serial: 1})
	userExpired := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-400 * day), notAfter: testNow.Add(-day), serial: 2})
	machineNew := newCandidate(t, certSpec{cn: "Jane Doe", notBefore: testNow.Add(-day), serial: 3})
	user := StorePath{CurrentUser, "My"}
	machine := StorePath{LocalMachine, "My"}

	stores := func(m map[StorePath][]Candidate) func(StorePath) ([]Candidate, error) {
		return func(p StorePath) ([]Candidate, error) { return m[p], nil }
	}

	t.Run("user store wins over a newer machine certificate", func(t *testing.T) {
		got, from, err := Selector{Subject: "Jane"}.search(stores(map[StorePath][]Candidate{user: {userOld}, machine: {machineNew}}), testNow, nil)
		if err != nil || got.Cert != userOld.Cert || from != user {
			t.Fatalf("got %v from %v, err %v", got.Cert, from, err)
		}
	})

	t.Run("falls back to the machine store when the user store has nothing eligible", func(t *testing.T) {
		got, from, err := Selector{Subject: "Jane"}.search(stores(map[StorePath][]Candidate{user: {userExpired}, machine: {machineNew}}), testNow, nil)
		if err != nil || got.Cert != machineNew.Cert || from != machine {
			t.Fatalf("got %v from %v, err %v", got.Cert, from, err)
		}
	})

	t.Run("a configured store is the only store searched", func(t *testing.T) {
		_, _, err := Selector{Subject: "Jane", Store: `LocalMachine\My`}.search(stores(map[StorePath][]Candidate{user: {userOld}}), testNow, nil)
		var none *NoEligibleError
		if !errors.As(err, &none) || len(none.Searched) != 1 || none.Searched[0] != machine {
			t.Fatalf("err = %v, want NoEligibleError naming only LocalMachine\\My", err)
		}
	})

	t.Run("no eligible certificate lists every rejection by store", func(t *testing.T) {
		machineServerOnly := newCandidate(t, certSpec{cn: "Jane Doe", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, serial: 4})
		_, _, err := Selector{Subject: "Jane"}.search(stores(map[StorePath][]Candidate{user: {userExpired}, machine: {machineServerOnly}}), testNow, nil)
		var none *NoEligibleError
		if !errors.As(err, &none) || len(none.Rejected) != 2 {
			t.Fatalf("err = %v", err)
		}
		msg := err.Error()
		for _, want := range []string{`CurrentUser\My`, `LocalMachine\My`, "expired", "client authentication", userExpired.Thumbprint()} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("a store read error stops the search", func(t *testing.T) {
		boom := errors.New("access denied")
		_, _, err := Selector{Subject: "Jane"}.search(func(StorePath) ([]Candidate, error) { return nil, boom }, testNow, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want it to wrap the read error", err)
		}
	})
}
