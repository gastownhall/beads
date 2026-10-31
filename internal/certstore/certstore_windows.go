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
// a handshake does: that the first store with an eligible certificate holds
// one whose key opens and signs, and it selects the most recently issued such
// certificate. A certificate whose key cannot be opened or cannot sign (an
// orphaned key, one only a legacy CryptoAPI provider can open, one that needs
// a PIN prompt) is skipped for the next newest, and appears with its reason in
// the error when no certificate is usable. A handshake may still choose an
// older certificate when the server's acceptable CAs or signature schemes, or
// what the newest key can sign, rule out the newest.
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
	pkcs1Only := id.signer.pkcs1Only
	r.done(id)
	if pkcs1Only && (cfg.MaxVersion == 0 || cfg.MaxVersion > tls.VersionTLS12) {
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

// identity is a resolved client certificate. refs and retired are guarded
// by the resolver's mu.
type identity struct {
	store  StorePath
	leaf   *x509.Certificate
	chain  [][]byte
	signer *signer

	// refs counts the holders of the identity: a handshake from certificate
	// selection until it ends, or a caller of resolve until it calls done.
	refs int
	// retired is set once the identity leaves the cache. Its key is released
	// when the last holder lets go.
	retired bool
}

func (id *identity) tlsCertificate() *tls.Certificate {
	c := &tls.Certificate{Certificate: id.chain, PrivateKey: id.signer, Leaf: id.leaf}
	if id.signer.pkcs1Only {
		c.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.PKCS1WithSHA256, tls.PKCS1WithSHA384, tls.PKCS1WithSHA512}
	}
	return c
}

// retire takes id out of service and reports whether nothing holds it, in
// which case the caller releases its signer after unlocking. The resolver's
// mu must be held.
func (id *identity) retire() bool {
	if id.retired {
		return false
	}
	id.retired = true
	return id.refs == 0
}

// cacheKey identifies a certificate in one store. The same certificate can
// sit in two stores with different private keys.
type cacheKey struct {
	store StorePath
	thumb string
}

type resolver struct {
	sel  Selector
	open storeOpener
	now  func() time.Time

	mu     sync.Mutex
	cache  map[cacheKey]*identity
	closed bool
}

var errClosed = errors.New("certstore: the client certificate configuration was closed")

// Close takes every identity out of service and refuses later handshakes.
// An identity nothing holds is released at once; one a handshake holds is
// released when that handshake ends.
func (r *resolver) Close() error {
	r.mu.Lock()
	r.closed = true
	var free []*identity
	for k, id := range r.cache {
		delete(r.cache, k)
		if id.retire() {
			free = append(free, id)
		}
	}
	r.mu.Unlock()
	for _, id := range free {
		id.signer.release()
	}
	return nil
}

// done drops a reference resolve returned, and releases the identity when it
// was retired and this was the last reference.
func (r *resolver) done(id *identity) {
	r.mu.Lock()
	id.refs--
	free := id.retired && id.refs == 0
	r.mu.Unlock()
	if free {
		id.signer.release()
	}
}

func newResolver(sel Selector) *resolver {
	return &resolver{sel: sel, open: openSystemStore, now: time.Now, cache: map[cacheKey]*identity{}}
}

func (r *resolver) getClientCertificate(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	id, err := r.resolve(cri)
	if err != nil {
		return nil, err
	}
	// The handshake holds the identity until it ends, so a renewal selected
	// by another handshake meanwhile cannot release the key before this one
	// has signed. crypto/tls cancels this context when the handshake ends.
	if ctx := cri.Context(); ctx != nil {
		go func() {
			<-ctx.Done()
			r.done(id)
		}()
	} else {
		r.done(id)
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
// failed. The caller holds the returned identity and must call done when it
// no longer needs the key. cri is nil for the eager resolve in Configure; at
// a handshake it supplies the server's acceptable CAs and signature schemes.
//
// Candidates are ranked first; the winner's key is then opened (or its cached
// identity reused). A certificate whose key cannot be opened or cannot make
// the test signature is excluded and the ranking runs again. At a handshake,
// candidates are judged by their public keys before ranking, and the winner
// is judged again with the signature schemes its key actually supports, so a
// key that cannot sign for this server (an RSA key without PSS under TLS 1.3)
// is excluded the same way. Exclusions are per store: the same certificate in
// another store may have a working key.
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
	unusable := map[cacheKey]error{} // why a certificate's key in a store cannot be used
	accept := func(store StorePath, c Candidate) error {
		if err, ok := unusable[cacheKey{store, c.Thumbprint()}]; ok {
			return err
		}
		if cri == nil {
			return nil
		}
		chain, err := r.chainFor(cacheKey{store, c.Thumbprint()}, opened[store])
		if err != nil {
			return fmt.Errorf("building its certificate chain: %w", err)
		}
		if err := cri.SupportsCertificate(&tls.Certificate{Certificate: chain, PrivateKey: publicKeyOnly{c.Cert.PublicKey}, Leaf: c.Cert}); err != nil {
			return fmt.Errorf("not acceptable to the server: %w", err)
		}
		return nil
	}
	for {
		cand, from, err := r.sel.search(open, r.now(), accept)
		if err != nil {
			return nil, err
		}
		key := cacheKey{from, cand.Thumbprint()}
		id, err := r.identityFor(cand, key, opened[from])
		if errors.Is(err, errClosed) {
			return nil, err
		}
		if err != nil {
			unusable[key] = err
			continue
		}
		if cri != nil {
			if err := cri.SupportsCertificate(id.tlsCertificate()); err != nil {
				pkcs1Only := id.signer.pkcs1Only
				r.done(id)
				if pkcs1Only && cri.Version == tls.VersionTLS13 {
					err = errors.New("its key cannot make RSA-PSS signatures, which TLS 1.3 requires")
				} else {
					err = fmt.Errorf("its key cannot make a signature the server accepts: %w", err)
				}
				unusable[key] = err
				continue
			}
		}
		r.retireOthers(key)
		return id, nil
	}
}

// identityFor returns the cached identity of cand, or opens its key, with a
// reference the caller holds. Building the chain and the key's test
// signature, which can wait on a smart card or a TPM, run without the
// resolver's lock, so other handshakes are not held behind them.
func (r *resolver) identityFor(cand Candidate, key cacheKey, o *openedStore) (*identity, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errClosed
	}
	var stale *identity
	if id, ok := r.cache[key]; ok {
		if !id.signer.failed.Load() {
			id.refs++
			r.mu.Unlock()
			return id, nil
		}
		delete(r.cache, key)
		if id.retire() {
			stale = id
		}
	}
	r.mu.Unlock()
	if stale != nil {
		stale.signer.release()
	}

	ctx := windows.CertDuplicateCertificateContext(o.ctxs[key.thumb])
	chain, err := buildChain(ctx, o.store)
	if err != nil {
		_ = windows.CertFreeCertificateContext(ctx)
		return nil, fmt.Errorf("building its certificate chain: %w", err)
	}
	sg, err := acquireSigner(ctx, cand.Cert)
	if err != nil {
		return nil, err
	}
	id := &identity{store: key.store, leaf: cand.Cert, chain: chain, signer: sg, refs: 1}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		sg.release()
		return nil, errClosed
	}
	if cur, ok := r.cache[key]; ok {
		if !cur.signer.failed.Load() {
			// Another handshake opened the same key meanwhile; keep its.
			cur.refs++
			r.mu.Unlock()
			sg.release()
			return cur, nil
		}
		delete(r.cache, key)
		if cur.retire() {
			stale = cur
		}
	}
	r.cache[key] = id
	r.mu.Unlock()
	if stale != nil {
		stale.signer.release()
	}
	return id, nil
}

// retireOthers takes every cached identity but keep's out of service, so a
// certificate that a renewal superseded does not hold its key until Close.
// Each is released once no handshake holds it.
func (r *resolver) retireOthers(keep cacheKey) {
	var free []*identity
	r.mu.Lock()
	for k, id := range r.cache {
		if k != keep {
			delete(r.cache, k)
			if id.retire() {
				free = append(free, id)
			}
		}
	}
	r.mu.Unlock()
	for _, id := range free {
		id.signer.release()
	}
}

// chainFor returns the chain of the certificate key names: the cached
// identity's, or one built from the store read in this resolve.
func (r *resolver) chainFor(key cacheKey, o *openedStore) ([][]byte, error) {
	r.mu.Lock()
	id, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return id.chain, nil
	}
	if o != nil {
		if ctx, ok := o.ctxs[key.thumb]; ok {
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
