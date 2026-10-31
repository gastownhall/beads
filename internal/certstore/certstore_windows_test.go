//go:build windows

package certstore

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The fixtures never touch a real certificate store or persist a key: each
// key is an ephemeral CNG key (created with no name), and each certificate
// lives in an in-memory store with the key handle attached as the
// CERT_NCRYPT_KEY_HANDLE_PROP_ID property.

var (
	procNCryptOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	procNCryptCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	procNCryptSetProperty         = ncrypt.NewProc("NCryptSetProperty")
	procNCryptFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	procNCryptExportKey           = ncrypt.NewProc("NCryptExportKey")
	procNCryptFreeObject          = ncrypt.NewProc("NCryptFreeObject")
	procCertSetContextProperty    = crypt32.NewProc("CertSetCertificateContextProperty")
)

func ncryptCall(t *testing.T, p *windows.LazyProc, args ...uintptr) {
	t.Helper()
	if r, _, _ := p.Call(args...); r != 0 {
		t.Fatalf("%s: %v", p.Name, ncryptStatus(r))
	}
}

func utf16(t *testing.T, s string) *uint16 {
	t.Helper()
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newEphemeralKey creates a CNG key that is never written to disk and
// returns its handle and public key. alg is "RSA" or "ECDSA_P256".
func newEphemeralKey(t *testing.T, alg string) (uintptr, crypto.PublicKey) {
	t.Helper()
	var prov, key uintptr
	ncryptCall(t, procNCryptOpenStorageProvider, uintptr(unsafe.Pointer(&prov)), uintptr(unsafe.Pointer(utf16(t, "Microsoft Software Key Storage Provider"))), 0)
	defer procNCryptFreeObject.Call(prov) //nolint:errcheck
	ncryptCall(t, procNCryptCreatePersistedKey, prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(utf16(t, alg))), 0, 0, 0)
	if alg == "RSA" {
		bits := uint32(2048)
		ncryptCall(t, procNCryptSetProperty, key, uintptr(unsafe.Pointer(utf16(t, "Length"))), uintptr(unsafe.Pointer(&bits)), 4, 0)
	}
	ncryptCall(t, procNCryptFinalizeKey, key, 0)

	blobType := "RSAPUBLICBLOB"
	if alg != "RSA" {
		blobType = "ECCPUBLICBLOB"
	}
	var n uint32
	ncryptCall(t, procNCryptExportKey, key, 0, uintptr(unsafe.Pointer(utf16(t, blobType))), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	buf := make([]byte, n)
	ncryptCall(t, procNCryptExportKey, key, 0, uintptr(unsafe.Pointer(utf16(t, blobType))), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0)
	if alg == "RSA" {
		// BCRYPT_RSAKEY_BLOB: Magic, BitLength, cbPublicExp, cbModulus, cbPrime1, cbPrime2.
		cbExp := binary.LittleEndian.Uint32(buf[8:])
		cbMod := binary.LittleEndian.Uint32(buf[12:])
		e := new(big.Int).SetBytes(buf[24 : 24+cbExp])
		m := new(big.Int).SetBytes(buf[24+cbExp : 24+cbExp+cbMod])
		return key, &rsa.PublicKey{N: m, E: int(e.Int64())}
	}
	// BCRYPT_ECCKEY_BLOB: Magic, cbKey, X, Y.
	cb := binary.LittleEndian.Uint32(buf[4:])
	return key, &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(buf[8 : 8+cb]), Y: new(big.Int).SetBytes(buf[8+cb : 8+2*cb])}
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial atomic.Int64

func newTestCA(t *testing.T, name string, parent *testCA) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial.Add(1)),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-48 * time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	issuer, signKey := tmpl, any(key)
	if parent != nil {
		issuer, signKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

type leafSpec struct {
	subject   pkix.Name
	notBefore time.Time
	eku       []x509.ExtKeyUsage
}

func (ca *testCA) issue(t *testing.T, pub crypto.PublicKey, s leafSpec) []byte {
	t.Helper()
	if s.notBefore.IsZero() {
		s.notBefore = time.Now().Add(-time.Hour)
	}
	if s.eku == nil {
		s.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)),
		Subject:      s.subject,
		NotBefore:    s.notBefore,
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  s.eku,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

type memStore struct{ h windows.Handle }

func newMemStore(t *testing.T) *memStore {
	t.Helper()
	h, err := windows.CertOpenStore(windows.CERT_STORE_PROV_MEMORY, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CertCloseStore(h, 0) })
	return &memStore{h: h}
}

// add puts der into the store and, when key is non-zero, associates the key
// with it.
func (m *memStore) add(t *testing.T, der []byte, key uintptr) {
	t.Helper()
	ctx, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CertFreeCertificateContext(ctx) //nolint:errcheck
	var added *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(m.h, ctx, windows.CERT_STORE_ADD_ALWAYS, &added); err != nil {
		t.Fatal(err)
	}
	defer windows.CertFreeCertificateContext(added) //nolint:errcheck
	if key != 0 {
		// pvData is the handle itself for this property, not a pointer to it.
		if r, _, err := procCertSetContextProperty.Call(uintptr(unsafe.Pointer(added)), certNCryptKeyHandlePropID, 0, key); r == 0 {
			t.Fatalf("CertSetCertificateContextProperty: %v", err)
		}
	}
}

// cryptKeyProvInfo is CRYPT_KEY_PROV_INFO.
type cryptKeyProvInfo struct {
	containerName *uint16
	provName      *uint16
	provType      uint32
	flags         uint32
	provParamCnt  uint32
	provParams    uintptr
	keySpec       uint32
}

// addOrphaned puts der into the store with a key association that names a
// CNG key container that does not exist, as a re-enrolment can leave behind.
func (m *memStore) addOrphaned(t *testing.T, der []byte) {
	t.Helper()
	ctx, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CertFreeCertificateContext(ctx) //nolint:errcheck
	var added *windows.CertContext
	if err := windows.CertAddCertificateContextToStore(m.h, ctx, windows.CERT_STORE_ADD_ALWAYS, &added); err != nil {
		t.Fatal(err)
	}
	defer windows.CertFreeCertificateContext(added) //nolint:errcheck
	info := cryptKeyProvInfo{
		containerName: utf16(t, fmt.Sprintf("certstore-test-missing-%d", serial.Add(1))),
		provName:      utf16(t, "Microsoft Software Key Storage Provider"),
	}
	if r, _, err := procCertSetContextProperty.Call(uintptr(unsafe.Pointer(added)), certKeyProvInfoPropID, 0, uintptr(unsafe.Pointer(&info))); r == 0 {
		t.Fatalf("CertSetCertificateContextProperty(KEY_PROV_INFO): %v", err)
	}
}

// testResolver is a resolver whose stores are the given in-memory stores;
// any other store path is missing. It is closed when the test ends.
func testResolver(t *testing.T, sel Selector, stores map[StorePath]*memStore) *resolver {
	t.Helper()
	r := newResolver(sel)
	r.open = func(p StorePath) (windows.Handle, func(), error) {
		if m, ok := stores[p]; ok {
			return m.h, func() {}, nil
		}
		return 0, func() {}, nil
	}
	return r
}

// heldResolve resolves and, on success, drops the reference resolve returns
// when the test ends, before the resolver is closed.
func heldResolve(t *testing.T, r *resolver, cri *tls.CertificateRequestInfo) (*identity, error) {
	t.Helper()
	id, err := r.resolve(cri)
	if err == nil {
		t.Cleanup(func() { r.done(id) })
	}
	return id, err
}

var (
	userMy    = StorePath{CurrentUser, "My"}
	machineMy = StorePath{LocalMachine, "My"}
)

func jane(cn string) pkix.Name {
	return pkix.Name{CommonName: cn, OrganizationalUnit: []string{"Eng, Platform"}, Organization: []string{"Contoso"}, Country: []string{"US"}}
}

func TestDisplaySubjectMatchesWindowsForm(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "ECDSA_P256")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane Q. Doe")}), key)

	cands, err := readStore(store.h, map[string]*windows.CertContext{})
	if err != nil || len(cands) != 1 {
		t.Fatalf("readStore = %d candidates, %v", len(cands), err)
	}
	// The form PowerShell prints for X509Certificate2.Subject.
	want := `CN=Jane Q. Doe, OU="Eng, Platform", O=Contoso, C=US`
	if cands[0].Subject != want {
		t.Fatalf("subject = %q, want %q", cands[0].Subject, want)
	}
	if !cands[0].HasPrivateKey {
		t.Fatal("HasPrivateKey = false for a certificate with a key")
	}
}

func TestReadStoreWithoutKey(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	_, pub := newEphemeralKey(t, "ECDSA_P256")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), 0)
	cands, err := readStore(store.h, map[string]*windows.CertContext{})
	if err != nil || len(cands) != 1 || cands[0].HasPrivateKey {
		t.Fatalf("got %+v, %v; want one candidate without a private key", cands, err)
	}
}

func TestSignerSchemes(t *testing.T) {
	for _, alg := range []string{"RSA", "ECDSA_P256"} {
		t.Run(alg, func(t *testing.T) {
			ca := newTestCA(t, "root", nil)
			key, pub := newEphemeralKey(t, alg)
			store := newMemStore(t)
			store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
			id, err := heldResolve(t, testResolver(t, Selector{Subject: "jane"}, map[StorePath]*memStore{userMy: store}), nil)
			if err != nil {
				t.Fatal(err)
			}
			if id.signer.pkcs1Only {
				t.Fatal("software key reported no RSA-PSS support")
			}
			for _, h := range []crypto.Hash{crypto.SHA256, crypto.SHA384, crypto.SHA512} {
				d := h.New()
				d.Write([]byte("certstore"))
				digest := d.Sum(nil)
				switch p := pub.(type) {
				case *rsa.PublicKey:
					sig, err := id.signer.Sign(nil, digest, h)
					if err != nil || rsa.VerifyPKCS1v15(p, h, digest, sig) != nil {
						t.Errorf("%v PKCS#1 v1.5: %v", h, err)
					}
					for _, salt := range []int{rsa.PSSSaltLengthEqualsHash, rsa.PSSSaltLengthAuto, 20} {
						opts := &rsa.PSSOptions{SaltLength: salt, Hash: h}
						sig, err := id.signer.Sign(nil, digest, opts)
						verify := &rsa.PSSOptions{SaltLength: salt, Hash: h}
						if salt == rsa.PSSSaltLengthAuto {
							verify.SaltLength = rsa.PSSSaltLengthAuto
						}
						if err != nil || rsa.VerifyPSS(p, h, digest, sig, verify) != nil {
							t.Errorf("%v PSS salt %d: %v", h, salt, err)
						}
					}
				case *ecdsa.PublicKey:
					sig, err := id.signer.Sign(nil, digest, h)
					if err != nil || !ecdsa.VerifyASN1(p, digest, sig) {
						t.Errorf("%v ECDSA: %v", h, err)
					}
				}
			}
			if _, err := id.signer.Sign(nil, []byte{1, 2, 3}, crypto.SHA256); err == nil {
				t.Error("Sign accepted a digest of the wrong length")
			}
		})
	}
}

func TestResolveStoreOrderAndChain(t *testing.T) {
	root := newTestCA(t, "root", nil)
	inter := newTestCA(t, "issuing CA", root)
	user, machine := newMemStore(t), newMemStore(t)
	user.add(t, inter.cert.Raw, 0)
	user.add(t, root.cert.Raw, 0)

	expiredKey, expiredPub := newEphemeralKey(t, "ECDSA_P256")
	user.add(t, inter.issue(t, expiredPub, leafSpec{subject: jane("Jane Doe"), eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}), expiredKey)
	machineKey, machinePub := newEphemeralKey(t, "ECDSA_P256")
	machineDER := inter.issue(t, machinePub, leafSpec{subject: jane("Jane Doe")})
	machine.add(t, machineDER, machineKey)

	stores := map[StorePath]*memStore{userMy: user, machineMy: machine}
	id, err := heldResolve(t, testResolver(t, Selector{Subject: "Jane Doe"}, stores), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.store != machineMy {
		t.Fatalf("chosen from %v, want the machine store after the user store had nothing eligible", id.store)
	}

	userKey, userPub := newEphemeralKey(t, "ECDSA_P256")
	userDER := inter.issue(t, userPub, leafSpec{subject: jane("Jane Doe"), notBefore: time.Now().Add(-30 * 24 * time.Hour)})
	user.add(t, userDER, userKey)
	id, err = heldResolve(t, testResolver(t, Selector{Subject: "Jane Doe"}, stores), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.store != userMy || string(id.chain[0]) != string(userDER) {
		t.Fatalf("chosen from %v, want the older user-store certificate over the newer machine one", id.store)
	}
	if len(id.chain) != 2 || string(id.chain[1]) != string(inter.cert.Raw) {
		t.Fatalf("chain has %d certificates, want the leaf and the intermediate without the root", len(id.chain))
	}
}

// serve runs one TLS server handshake on a loopback listener that requires a
// client certificate verified against roots, and returns the client
// certificate the server received.
func serve(t *testing.T, roots *x509.CertPool) (addr string, got <-chan []*x509.Certificate) {
	t.Helper()
	srvCA := newTestCA(t, "server CA", nil)
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTmpl := &x509.Certificate{SerialNumber: big.NewInt(serial.Add(1)), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, srvCA.cert, &srvKey.PublicKey, srvCA.key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan []*x509.Certificate, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			ch <- nil
			return
		}
		defer c.Close()
		tc := c.(*tls.Conn)
		if err := tc.Handshake(); err != nil {
			ch <- nil
			return
		}
		ch <- tc.ConnectionState().PeerCertificates
	}()
	return ln.Addr().String(), ch
}

func dialWith(t *testing.T, addr string, r *resolver, maxVersion uint16) error {
	t.Helper()
	cfg := &tls.Config{InsecureSkipVerify: true, MaxVersion: maxVersion, GetClientCertificate: r.getClientCertificate} //nolint:gosec // G402: the test server's identity is not under test
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
}

func TestHandshakeWithIntermediateAgainstRootOnlyServer(t *testing.T) {
	root := newTestCA(t, "root", nil)
	inter := newTestCA(t, "issuing CA", root)
	for _, alg := range []string{"RSA", "ECDSA_P256"} {
		for _, v := range []uint16{tls.VersionTLS13, tls.VersionTLS12} {
			t.Run(alg+"/"+tls.VersionName(v), func(t *testing.T) {
				key, pub := newEphemeralKey(t, alg)
				store := newMemStore(t)
				store.add(t, inter.cert.Raw, 0)
				leaf := inter.issue(t, pub, leafSpec{subject: jane("Jane")})
				store.add(t, leaf, key)

				roots := x509.NewCertPool()
				roots.AddCert(root.cert)
				addr, got := serve(t, roots)
				r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
				if err := dialWith(t, addr, r, v); err != nil {
					t.Fatalf("handshake: %v", err)
				}
				peer := <-got
				if len(peer) == 0 || string(peer[0].Raw) != string(leaf) {
					t.Fatal("server did not receive the store certificate")
				}
			})
		}
	}
}

func TestHandshakeSkipsCertificateFromCAServerDoesNotAccept(t *testing.T) {
	trusted := newTestCA(t, "trusted root", nil)
	other := newTestCA(t, "other root", nil)
	store := newMemStore(t)
	oldKey, oldPub := newEphemeralKey(t, "ECDSA_P256")
	oldDER := trusted.issue(t, oldPub, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-10 * time.Hour)})
	store.add(t, oldDER, oldKey)
	newKey, newPub := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, other.issue(t, newPub, leafSpec{subject: jane("Jane")}), newKey)

	roots := x509.NewCertPool()
	roots.AddCert(trusted.cert)
	addr, got := serve(t, roots)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	if err := dialWith(t, addr, r, 0); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if peer := <-got; len(peer) == 0 || string(peer[0].Raw) != string(oldDER) {
		t.Fatal("want the older certificate issued under the CA the server accepts")
	}
}

func TestHandshakeSkipsKeyThatCannotSignForTLS13(t *testing.T) {
	trusted := newTestCA(t, "trusted root", nil)
	other := newTestCA(t, "other root", nil)
	store := newMemStore(t)
	oldKey, oldPub := newEphemeralKey(t, "ECDSA_P256")
	oldDER := trusted.issue(t, oldPub, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-10 * time.Hour)})
	store.add(t, oldDER, oldKey)
	rsaKey, rsaPub := newEphemeralKey(t, "RSA")
	store.add(t, trusted.issue(t, rsaPub, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-5 * time.Hour)}), rsaKey)
	newKey, newPub := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, other.issue(t, newPub, leafSpec{subject: jane("Jane")}), newKey)
	real := ncryptSignHash
	withSignHash(t, func(k uintptr, pad unsafe.Pointer, d []byte, f uintptr) ([]byte, error) {
		if f&bcryptPadPSS != 0 {
			return nil, nteNotSupported
		}
		return real(k, pad, d, f)
	})

	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	if id, err := heldResolve(t, r, nil); err != nil || id.signer.pkcs1Only {
		t.Fatalf("eager resolve: %v; want the newest (ECDSA) certificate, so no TLS 1.2 cap", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(trusted.cert)
	addr, got := serve(t, roots)
	if err := dialWith(t, addr, r, 0); err != nil {
		t.Fatalf("TLS 1.3 handshake: %v", err)
	}
	if peer := <-got; len(peer) == 0 || string(peer[0].Raw) != string(oldDER) {
		t.Fatal("want the oldest certificate: the newest is from a CA the server rejects, the next has a key without PSS")
	}
}

func TestRenewalPickedUpByLaterResolve(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	store := newMemStore(t)
	k1, p1 := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, ca.issue(t, p1, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-5 * time.Hour)}), k1)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	first, err := heldResolve(t, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	k2, p2 := newEphemeralKey(t, "ECDSA_P256")
	renewed := ca.issue(t, p2, leafSpec{subject: jane("Jane")})
	store.add(t, renewed, k2)
	second, err := heldResolve(t, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first || string(second.chain[0]) != string(renewed) {
		t.Fatal("a later resolve did not pick up the renewed certificate")
	}
	again, err := heldResolve(t, r, nil)
	if err != nil || again != second {
		t.Fatalf("an unchanged store did not reuse the cached identity: %v", err)
	}
}

func TestNewestCertificateWithUnusableKeyIsSkipped(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	store := newMemStore(t)
	goodKey, goodPub := newEphemeralKey(t, "ECDSA_P256")
	goodDER := ca.issue(t, goodPub, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-10 * time.Hour)})
	store.add(t, goodDER, goodKey)
	badKey, badPub := newEphemeralKey(t, "RSA")
	store.add(t, ca.issue(t, badPub, leafSpec{subject: jane("Jane")}), badKey)
	real := ncryptSignHash
	withSignHash(t, func(k uintptr, pad unsafe.Pointer, d []byte, f uintptr) ([]byte, error) {
		if k == badKey {
			return nil, nteSilentContext
		}
		return real(k, pad, d, f)
	})

	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	id, err := heldResolve(t, r, nil)
	if err != nil {
		t.Fatalf("resolve: %v; want the older certificate, whose key works", err)
	}
	if string(id.chain[0]) != string(goodDER) {
		t.Fatal("want the older certificate when the newest one's key needs a prompt")
	}
}

func TestSupersededIdentityReleasedWhenNothingHoldsIt(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	store := newMemStore(t)
	k1, p1 := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, ca.issue(t, p1, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-5 * time.Hour)}), k1)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	first, err := r.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	k2, p2 := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, ca.issue(t, p2, leafSpec{subject: jane("Jane")}), k2)
	second, err := r.resolve(nil)
	if err != nil || second == first {
		t.Fatalf("renewal not picked up: %v", err)
	}
	digest := sha512.Sum384([]byte("x"))
	if _, err := first.signer.Sign(nil, digest[:], crypto.SHA384); err != nil {
		t.Fatalf("the superseded key was released while still held: %v", err)
	}
	r.done(first)
	if _, err := first.signer.Sign(nil, digest[:], crypto.SHA384); err == nil {
		t.Fatal("the superseded key was still open after its last holder let go")
	}
	if _, err := second.signer.Sign(nil, digest[:], crypto.SHA384); err != nil {
		t.Fatalf("the current key was released: %v", err)
	}
	r.done(second)
	r.mu.Lock()
	cached := len(r.cache)
	r.mu.Unlock()
	if cached != 1 {
		t.Fatalf("cache holds %d identities, want 1", cached)
	}
}

// TestHandshakeKeepsKeyThroughRenewal pauses a handshake after certificate
// selection, lets another resolve pick a renewal (retiring the first
// certificate), then lets the handshake sign with the key it was given.
func TestHandshakeKeepsKeyThroughRenewal(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	store := newMemStore(t)
	k1, p1 := newEphemeralKey(t, "ECDSA_P256")
	oldDER := ca.issue(t, p1, leafSpec{subject: jane("Jane"), notBefore: time.Now().Add(-5 * time.Hour)})
	store.add(t, oldDER, k1)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})

	selected := make(chan struct{})
	proceed := make(chan struct{})
	var release sync.Once
	letProceed := func() { release.Do(func() { close(proceed) }) }
	t.Cleanup(letProceed)
	cfg := &tls.Config{InsecureSkipVerify: true, GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) { //nolint:gosec // G402: the test server's identity is not under test
		c, err := r.getClientCertificate(cri)
		close(selected)
		<-proceed
		return c, err
	}}
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	addr, got := serve(t, roots)
	dialed := make(chan error, 1)
	go func() {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, cfg)
		if err == nil {
			_ = c.Close()
		}
		dialed <- err
	}()
	select {
	case <-selected:
	case err := <-dialed:
		t.Fatalf("handshake ended before certificate selection: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no certificate selection within 10s")
	}

	k2, p2 := newEphemeralKey(t, "ECDSA_P256")
	store.add(t, ca.issue(t, p2, leafSpec{subject: jane("Jane")}), k2)
	renewed, err := r.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	r.done(renewed)
	letProceed()

	if err := <-dialed; err != nil {
		t.Fatalf("handshake whose certificate was superseded mid-handshake: %v", err)
	}
	if peer := <-got; len(peer) == 0 || string(peer[0].Raw) != string(oldDER) {
		t.Fatal("server did not receive the certificate selected for the handshake")
	}
}

// TestKeyFailureInOneStoreDoesNotRejectAnotherStoresCopy puts the same
// certificate in both stores: the user store's copy points at a key container
// that does not exist, the machine store's copy has a working key.
func TestKeyFailureInOneStoreDoesNotRejectAnotherStoresCopy(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "ECDSA_P256")
	der := ca.issue(t, pub, leafSpec{subject: jane("Jane")})
	user, machine := newMemStore(t), newMemStore(t)
	user.addOrphaned(t, der)
	machine.add(t, der, key)

	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: user, machineMy: machine})
	id, err := r.resolve(nil)
	if err != nil {
		t.Fatalf("resolve: %v; want the machine store's copy", err)
	}
	defer r.done(id)
	if id.store != machineMy {
		t.Fatalf("selected the copy in %s, want %s", id.store, machineMy)
	}
}

func TestConcurrentSignatures(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "RSA")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
	id, err := heldResolve(t, testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store}), nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("concurrent"))
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}
			sig, err := id.signer.Sign(nil, digest[:], opts)
			if err == nil {
				err = rsa.VerifyPSS(pub.(*rsa.PublicKey), crypto.SHA256, digest[:], sig, opts)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// withSignHash replaces the NCrypt call for one test.
func withSignHash(t *testing.T, f func(key uintptr, pad unsafe.Pointer, digest []byte, flags uintptr) ([]byte, error)) {
	t.Helper()
	orig := ncryptSignHash
	ncryptSignHash = f
	t.Cleanup(func() { ncryptSignHash = orig })
}

func TestFailedSignatureReopensKey(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "ECDSA_P256")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	first, err := r.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}

	real := ncryptSignHash
	const scardRemovedCard = ncryptStatus(0x80100069)
	var fail atomic.Bool
	fail.Store(true)
	withSignHash(t, func(k uintptr, pad unsafe.Pointer, d []byte, f uintptr) ([]byte, error) {
		if fail.Load() {
			return nil, scardRemovedCard
		}
		return real(k, pad, d, f)
	})
	digest := sha512.Sum384([]byte("x"))
	if _, err := first.signer.Sign(nil, digest[:], crypto.SHA384); !errors.Is(err, scardRemovedCard) {
		t.Fatalf("Sign error = %v, want the provider status", err)
	}
	fail.Store(false)
	r.done(first)

	second, err := heldResolve(t, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a signer whose signature failed was reused")
	}
	if _, err := first.signer.Sign(nil, digest[:], crypto.SHA384); err == nil {
		t.Fatal("the evicted signer still signs")
	}
	sig, err := second.signer.Sign(nil, digest[:], crypto.SHA384)
	if err != nil || !ecdsa.VerifyASN1(pub.(*ecdsa.PublicKey), digest[:], sig) {
		t.Fatalf("reopened signer: %v", err)
	}
	runtime.KeepAlive(key)
}

func TestKeyNeedingPromptIsAnError(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "RSA")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
	withSignHash(t, func(uintptr, unsafe.Pointer, []byte, uintptr) ([]byte, error) { return nil, nteSilentContext })
	_, err := heldResolve(t, testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store}), nil)
	if !errors.Is(err, nteSilentContext) || !strings.Contains(err.Error(), "PIN or consent prompt") {
		t.Fatalf("err = %v, want the prompt-needed error", err)
	}
}

func TestECDSAKeyNeedingPromptFailsConfigure(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "ECDSA_P256")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
	withSignHash(t, func(uintptr, unsafe.Pointer, []byte, uintptr) ([]byte, error) { return nil, nteSilentContext })
	cfg := &tls.Config{}
	_, err := configure(cfg, testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store}))
	if !errors.Is(err, nteSilentContext) {
		t.Fatalf("err = %v, want the prompt-needed error at setup", err)
	}
	if cfg.GetClientCertificate != nil {
		t.Fatal("a failed Configure set GetClientCertificate")
	}
}

func TestKeyWithoutPSSIsLimitedToTLS12(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "RSA")
	store := newMemStore(t)
	leaf := ca.issue(t, pub, leafSpec{subject: jane("Jane")})
	store.add(t, leaf, key)
	real := ncryptSignHash
	withSignHash(t, func(k uintptr, pad unsafe.Pointer, d []byte, f uintptr) ([]byte, error) {
		if f&bcryptPadPSS != 0 {
			return nil, nteNotSupported
		}
		return real(k, pad, d, f)
	})
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	id, err := heldResolve(t, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !id.signer.pkcs1Only {
		t.Fatal("NTE_NOT_SUPPORTED for PSS did not limit the key to PKCS#1 v1.5")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	addr, got := serve(t, roots)
	if err := dialWith(t, addr, r, tls.VersionTLS12); err != nil {
		t.Fatalf("TLS 1.2 handshake with PKCS#1 v1.5 only: %v", err)
	}
	if peer := <-got; len(peer) == 0 || string(peer[0].Raw) != string(leaf) {
		t.Fatal("server did not receive the certificate")
	}
	addr, _ = serve(t, roots)
	if err := dialWith(t, addr, r, tls.VersionTLS13); err == nil || !strings.Contains(err.Error(), "RSA-PSS") {
		t.Fatalf("TLS 1.3 handshake error = %v, want the RSA-PSS explanation", err)
	}
}

func TestConfigureCapsTLSForKeyWithoutPSS(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "RSA")
	store := newMemStore(t)
	leaf := ca.issue(t, pub, leafSpec{subject: jane("Jane")})
	store.add(t, leaf, key)
	real := ncryptSignHash
	withSignHash(t, func(k uintptr, pad unsafe.Pointer, d []byte, f uintptr) ([]byte, error) {
		if f&bcryptPadPSS != 0 {
			return nil, nteNotSupported
		}
		return real(k, pad, d, f)
	})
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)

	for _, preset := range []uint16{0, tls.VersionTLS13} {
		cfg := &tls.Config{InsecureSkipVerify: true, MaxVersion: preset} //nolint:gosec // G402: the test server's identity is not under test
		closer, err := configure(cfg, testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store}))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = closer.Close() })
		if cfg.MaxVersion != tls.VersionTLS12 {
			t.Fatalf("preset MaxVersion %#x: Configure left %#x, want TLS 1.2", preset, cfg.MaxVersion)
		}
		addr, got := serve(t, roots)
		c, err := tls.Dial("tcp", addr, cfg)
		if err != nil {
			t.Fatalf("handshake with the configured cap: %v", err)
		}
		if v := c.ConnectionState().Version; v != tls.VersionTLS12 {
			t.Errorf("negotiated %s, want TLS 1.2", tls.VersionName(v))
		}
		_ = c.Close()
		if peer := <-got; len(peer) == 0 || string(peer[0].Raw) != string(leaf) {
			t.Fatal("server did not receive the certificate")
		}
	}

	cfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // G402: as above
	withSignHash(t, real)
	closer, err := configure(cfg, testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store}))
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close() //nolint:errcheck
	if cfg.MaxVersion != 0 {
		t.Fatalf("a key with RSA-PSS support capped MaxVersion at %#x", cfg.MaxVersion)
	}
}

func TestCloseReleasesKeysAndRefusesHandshakes(t *testing.T) {
	ca := newTestCA(t, "root", nil)
	key, pub := newEphemeralKey(t, "ECDSA_P256")
	store := newMemStore(t)
	store.add(t, ca.issue(t, pub, leafSpec{subject: jane("Jane")}), key)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	cfg := &tls.Config{}
	closer, err := configure(cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("x"))
	if _, err := id.signer.Sign(nil, digest[:], crypto.SHA256); err != nil {
		t.Fatalf("Close released a key still held: %v", err)
	}
	r.done(id)
	if _, err := id.signer.Sign(nil, digest[:], crypto.SHA256); err == nil {
		t.Fatal("a signer still signs after Close and its last holder let go")
	}
	if _, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{Version: tls.VersionTLS13}); !errors.Is(err, errClosed) {
		t.Fatalf("handshake after Close: err = %v, want errClosed", err)
	}
	if len(r.cache) != 0 {
		t.Fatalf("%d identities still cached after Close", len(r.cache))
	}
}

// A CA rollover link certificate is self-issued (subject equals issuer) but
// signed by the old CA key. It must stay in the chain: a server that trusts
// only the old root needs it to reach the new key.
func TestRolloverLinkCertificateStaysInChain(t *testing.T) {
	oldKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := pkix.Name{CommonName: "Rollover Root"}
	caTmpl := func() *x509.Certificate {
		return &x509.Certificate{SerialNumber: big.NewInt(serial.Add(1)), Subject: name,
			NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
			IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	}
	parse := func(der []byte, err error) *x509.Certificate {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	oldTmpl := caTmpl()
	oldRoot := parse(x509.CreateCertificate(rand.Reader, oldTmpl, oldTmpl, &oldKey.PublicKey, oldKey))
	link := parse(x509.CreateCertificate(rand.Reader, caTmpl(), oldRoot, &newKey.PublicKey, oldKey))
	if selfSigned(link) || !selfSigned(oldRoot) {
		t.Fatalf("fixture: selfSigned(link)=%v selfSigned(oldRoot)=%v", selfSigned(link), selfSigned(oldRoot))
	}

	key, pub := newEphemeralKey(t, "ECDSA_P256")
	leaf := (&testCA{cert: link, key: newKey}).issue(t, pub, leafSpec{subject: jane("Jane")})
	store := newMemStore(t)
	store.add(t, oldRoot.Raw, 0)
	store.add(t, link.Raw, 0)
	store.add(t, leaf, key)

	roots := x509.NewCertPool()
	roots.AddCert(oldRoot)
	addr, got := serve(t, roots)
	r := testResolver(t, Selector{Subject: "Jane"}, map[StorePath]*memStore{userMy: store})
	if err := dialWith(t, addr, r, 0); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	peer := <-got
	if len(peer) != 2 || string(peer[1].Raw) != string(link.Raw) {
		t.Fatalf("server received %d certificates, want the leaf and the rollover link", len(peer))
	}
}

// TestOpenSystemStore is the one test that opens a real system store: it
// reads the user's CurrentUser\My read-only and changes nothing.
func TestOpenSystemStore(t *testing.T) {
	h, closeStore, err := openSystemStore(StorePath{CurrentUser, "certstore-test-no-such-store"})
	if err != nil || h != 0 {
		t.Fatalf("missing store: handle %v, err %v; want zero handle and no error", h, err)
	}
	closeStore()

	h, closeStore, err = openSystemStore(userMy)
	if err != nil || h == 0 {
		t.Fatalf(`CurrentUser\My: handle %v, err %v`, h, err)
	}
	defer closeStore()
	ctxs := map[string]*windows.CertContext{}
	defer func() {
		for _, c := range ctxs {
			_ = windows.CertFreeCertificateContext(c)
		}
	}()
	if _, err := readStore(h, ctxs); err != nil {
		t.Fatalf(`reading CurrentUser\My: %v`, err)
	}
}
