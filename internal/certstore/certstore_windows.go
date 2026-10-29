//go:build windows

package certstore

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32                               = windows.NewLazySystemDLL("crypt32.dll")
	procCertGetCertificateContextProperty = crypt32.NewProc("CertGetCertificateContextProperty")
)

const (
	certKeyProvInfoPropID     = 2
	certNCryptKeyHandlePropID = 78

	certX500NameStr        = 3
	certNameStrReverseFlag = 0x02000000

	certChainCacheOnlyURLRetrieval = 0x00000004
	certChainDisableAIA            = 0x00002000
)

// Configure resolves the selector once and sets cfg.GetClientCertificate to
// resolve it again at each handshake, so a renewed certificate is used
// without reconfiguring. Call it before the config is registered or cloned.
//
// The eager resolve has no server CertificateRequest, so it proves less than
// a handshake does: that the selector names an eligible certificate in the
// first store that has one, and that the most recently issued such
// certificate's key opens and signs. It fails when either is false.
// A handshake may still choose an older certificate when the server's
// acceptable CAs or signature schemes, or what the newest key can sign, rule
// out the newest.
//
// When that key cannot make RSA-PSS signatures (NTE_NOT_SUPPORTED), which
// TLS 1.3 requires for RSA, Configure caps cfg.MaxVersion at TLS 1.2. The cap
// is decided here only: under TLS 1.3 a handshake skips any such key, and
// fails when no other eligible certificate remains.
//
// The returned Closer releases the certificate references and key handles
// the configuration holds. Handshakes after Close fail.
func (s Selector) Configure(cfg *tls.Config) (io.Closer, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return configure(cfg, newResolver(s))
}

func configure(cfg *tls.Config, r *resolver) (io.Closer, error) {
	id, err := r.resolve(nil)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	if id.signer.pkcs1Only && (cfg.MaxVersion == 0 || cfg.MaxVersion > tls.VersionTLS12) {
		cfg.MaxVersion = tls.VersionTLS12
	}
	cfg.GetClientCertificate = r.getClientCertificate
	return r, nil
}

// storeOpener opens a store for reading. A store that does not exist yields
// a zero handle and no error. closeStore releases what open returned.
type storeOpener func(StorePath) (store windows.Handle, closeStore func(), err error)

func openSystemStore(p StorePath) (windows.Handle, func(), error) {
	var loc uint32
	switch p.Location {
	case CurrentUser:
		loc = windows.CERT_SYSTEM_STORE_CURRENT_USER
	case LocalMachine:
		loc = windows.CERT_SYSTEM_STORE_LOCAL_MACHINE
	default:
		return 0, nil, fmt.Errorf("unknown store location %v", p.Location)
	}
	name, err := windows.UTF16PtrFromString(p.Name)
	if err != nil {
		return 0, nil, err
	}
	flags := loc | windows.CERT_STORE_READONLY_FLAG | windows.CERT_STORE_OPEN_EXISTING_FLAG
	// windows.CertOpenStore takes the name as a plain uintptr, so nothing
	// keeps the UTF-16 buffer alive during the call except the KeepAlive.
	h, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM_W, 0, 0, flags, uintptr(unsafe.Pointer(name))) // nolint:gosec // G103: syscall argument, kept alive below
	runtime.KeepAlive(name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return 0, func() {}, nil
	}
	if err != nil {
		return 0, nil, err
	}
	return h, func() { _ = windows.CertCloseStore(h, 0) }, nil
}

// identity is a resolved client certificate.
type identity struct {
	store  StorePath
	leaf   *x509.Certificate
	chain  [][]byte
	signer *signer
}

func (id *identity) tlsCertificate() *tls.Certificate {
	c := &tls.Certificate{Certificate: id.chain, PrivateKey: id.signer, Leaf: id.leaf}
	if id.signer.pkcs1Only {
		c.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.PKCS1WithSHA256, tls.PKCS1WithSHA384, tls.PKCS1WithSHA512}
	}
	return c
}

type resolver struct {
	sel  Selector
	open storeOpener
	now  func() time.Time

	mu     sync.Mutex
	cache  map[string]*identity // by thumbprint
	closed bool
}

var errClosed = errors.New("certstore: the client certificate configuration was closed")

// Close releases every cached identity. Signatures in progress finish first.
func (r *resolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for thumb, id := range r.cache {
		id.signer.release()
		delete(r.cache, thumb)
	}
	return nil
}

func newResolver(sel Selector) *resolver {
	return &resolver{sel: sel, open: openSystemStore, now: time.Now, cache: map[string]*identity{}}
}

func (r *resolver) getClientCertificate(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	id, err := r.resolve(cri)
	if err != nil {
		return nil, err
	}
	return id.tlsCertificate(), nil
}

// openedStore is one store read during a resolve, with a reference to each
// enumerated certificate so the winner's key and chain can be read after
// ranking.
type openedStore struct {
	store windows.Handle
	ctxs  map[string]*windows.CertContext
	cands []Candidate
	err   error
	close func()
}

func (o *openedStore) release() {
	for _, c := range o.ctxs {
		_ = windows.CertFreeCertificateContext(c)
	}
	if o.close != nil {
		o.close()
	}
}

// publicKeyOnly lets CertificateRequestInfo.SupportsCertificate judge a
// candidate by its public key without opening the private key.
type publicKeyOnly struct{ pub crypto.PublicKey }

func (p publicKeyOnly) Public() crypto.PublicKey { return p.pub }

func (p publicKeyOnly) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("certstore: not a signing key")
}

// resolve selects the certificate and returns its identity, reusing the
// cached identity of a certificate used before unless its last signature
// failed. cri is nil for the eager resolve in Configure; at a handshake it
// supplies the server's acceptable CAs and signature schemes.
//
// At a handshake, candidates are first judged by their public keys. The
// winner's key is then opened and judged again with the signature schemes it
// actually supports; a key that cannot sign for this server (an RSA key
// without PSS under TLS 1.3) is excluded and the ranking runs again. A key
// that cannot be opened is an error, never a reason to try an older
// certificate.
func (r *resolver) resolve(cri *tls.CertificateRequestInfo) (*identity, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, errClosed
	}
	opened := map[StorePath]*openedStore{}
	defer func() {
		for _, o := range opened {
			o.release()
		}
	}()
	open := func(p StorePath) ([]Candidate, error) {
		if o, ok := opened[p]; ok {
			return o.cands, o.err
		}
		h, closeStore, err := r.open(p)
		if err != nil {
			return nil, err
		}
		o := &openedStore{store: h, ctxs: map[string]*windows.CertContext{}, close: closeStore}
		opened[p] = o
		if h != 0 {
			o.cands, o.err = readStore(h, o.ctxs)
		}
		return o.cands, o.err
	}
	unusable := map[string]string{} // thumbprint → why its key cannot sign for this server
	var accept func(Candidate) (bool, string)
	if cri != nil {
		accept = func(c Candidate) (bool, string) {
			if why, ok := unusable[c.Thumbprint()]; ok {
				return false, why
			}
			chain, err := r.chainFor(c, opened)
			if err != nil {
				return false, "building its certificate chain failed: " + err.Error()
			}
			if err := cri.SupportsCertificate(&tls.Certificate{Certificate: chain, PrivateKey: publicKeyOnly{c.Cert.PublicKey}, Leaf: c.Cert}); err != nil {
				return false, "not acceptable to the server: " + err.Error()
			}
			return true, ""
		}
	}
	for {
		cand, from, err := r.sel.search(open, r.now(), accept)
		if err != nil {
			return nil, err
		}
		id, err := r.identityFor(cand, from, opened[from])
		if err != nil || cri == nil {
			return id, err
		}
		if err := cri.SupportsCertificate(id.tlsCertificate()); err != nil {
			why := "its key cannot make a signature the server accepts: " + err.Error()
			if id.signer.pkcs1Only && cri.Version == tls.VersionTLS13 {
				why = "its key cannot make RSA-PSS signatures, which TLS 1.3 requires"
			}
			unusable[cand.Thumbprint()] = why
			continue
		}
		return id, nil
	}
}

// identityFor returns the cached identity of cand, or opens its key.
func (r *resolver) identityFor(cand Candidate, from StorePath, o *openedStore) (*identity, error) {
	thumb := cand.Thumbprint()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errClosed
	}
	if id, ok := r.cache[thumb]; ok {
		if !id.signer.failed.Load() {
			return id, nil
		}
		delete(r.cache, thumb)
		id.signer.release()
	}
	ctx := windows.CertDuplicateCertificateContext(o.ctxs[thumb])
	chain, err := buildChain(ctx, o.store)
	if err != nil {
		_ = windows.CertFreeCertificateContext(ctx)
		return nil, fmt.Errorf("certstore: client certificate %s in %s: building its certificate chain: %w", thumb, from, err)
	}
	sg, err := acquireSigner(ctx, cand.Cert)
	if err != nil {
		return nil, fmt.Errorf("certstore: client certificate %s (%s) in %s: %w", thumb, cand.Subject, from, err)
	}
	id := &identity{store: from, leaf: cand.Cert, chain: chain, signer: sg}
	r.cache[thumb] = id
	return id, nil
}

func (r *resolver) chainFor(c Candidate, opened map[StorePath]*openedStore) ([][]byte, error) {
	thumb := c.Thumbprint()
	r.mu.Lock()
	id, ok := r.cache[thumb]
	r.mu.Unlock()
	if ok {
		return id.chain, nil
	}
	for _, o := range opened {
		if ctx, ok := o.ctxs[thumb]; ok {
			return buildChain(ctx, o.store)
		}
	}
	return nil, errors.New("certificate is no longer open")
}

// readStore parses every certificate in the store into a Candidate and keeps
// a reference to each in ctxs, keyed by thumbprint. Certificates that do not
// parse are skipped: they cannot be a usable TLS client certificate.
func readStore(store windows.Handle, ctxs map[string]*windows.CertContext) ([]Candidate, error) {
	var cands []Candidate
	var ctx *windows.CertContext
	for {
		next, err := windows.CertEnumCertificatesInStore(store, ctx)
		if next == nil {
			if err != nil && !errors.Is(err, windows.Errno(windows.CRYPT_E_NOT_FOUND)) {
				return nil, err
			}
			return cands, nil
		}
		ctx = next
		cert, err := x509.ParseCertificate(encoded(ctx))
		if err != nil {
			continue
		}
		c := Candidate{Cert: cert, Subject: displaySubject(ctx), HasPrivateKey: hasPrivateKey(ctx)}
		if _, dup := ctxs[c.Thumbprint()]; !dup {
			ctxs[c.Thumbprint()] = windows.CertDuplicateCertificateContext(ctx)
			cands = append(cands, c)
		}
	}
}

// encoded copies the DER certificate out of a store context.
func encoded(ctx *windows.CertContext) []byte {
	return append([]byte(nil), unsafe.Slice(ctx.EncodedCert, ctx.Length)...) // nolint:gosec // G103: reads the encoded certificate the context owns
}

// displaySubject returns the subject in the form Windows shows, which is
// what X509Certificate2.Subject returns in PowerShell.
func displaySubject(ctx *windows.CertContext) string {
	strType := uint32(certX500NameStr | certNameStrReverseFlag)
	n := windows.CertGetNameString(ctx, windows.CERT_NAME_RDN_TYPE, 0, unsafe.Pointer(&strType), nil, 0) // nolint:gosec // G103: syscall argument
	if n <= 1 {
		return ""
	}
	buf := make([]uint16, n)
	n = windows.CertGetNameString(ctx, windows.CERT_NAME_RDN_TYPE, 0, unsafe.Pointer(&strType), &buf[0], n) // nolint:gosec // G103: syscall argument
	return windows.UTF16ToString(buf[:n])
}

func hasPrivateKey(ctx *windows.CertContext) bool {
	for _, prop := range []uintptr{certKeyProvInfoPropID, certNCryptKeyHandlePropID} {
		var size uint32
		// nolint:gosec // G103: pointer arguments converted inside the LazyProc.Call expression
		r, _, _ := procCertGetCertificateContextProperty.Call(uintptr(unsafe.Pointer(ctx)), prop, 0, uintptr(unsafe.Pointer(&size)))
		if r != 0 {
			return true
		}
	}
	return false
}

// buildChain returns the DER chain to send: the leaf and the intermediates
// Windows finds locally, without a self-signed root. It never fetches an
// issuer over the network and does not check revocation; the server
// verifies the chain. An issuer that is not available locally leaves the
// chain short, which the server reports as an unknown authority.
func buildChain(ctx *windows.CertContext, store windows.Handle) ([][]byte, error) {
	para := windows.CertChainPara{}
	para.Size = uint32(unsafe.Sizeof(para)) // nolint:gosec // G103: struct size the API requires
	var chainCtx *windows.CertChainContext
	if err := windows.CertGetCertificateChain(0, ctx, nil, store, &para, certChainCacheOnlyURLRetrieval|certChainDisableAIA, 0, &chainCtx); err != nil {
		return nil, err
	}
	defer windows.CertFreeCertificateChain(chainCtx)
	out := [][]byte{encoded(ctx)}
	if chainCtx.ChainCount == 0 {
		return out, nil
	}
	simple := *chainCtx.Chains
	elems := unsafe.Slice(simple.Elements, simple.NumElements) // nolint:gosec // G103: walks the chain the API returned
	for _, e := range elems[1:] {
		der := encoded(e.CertContext)
		c, err := x509.ParseCertificate(der)
		if err != nil || selfSigned(c) {
			continue
		}
		out = append(out, der)
	}
	return out, nil
}

// selfSigned reports whether c is signed by its own key. A self-issued
// certificate (subject equals issuer) signed by another key, such as a CA
// rollover link, is not self-signed and must stay in the chain.
func selfSigned(c *x509.Certificate) bool {
	return bytes.Equal(c.RawSubject, c.RawIssuer) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}
