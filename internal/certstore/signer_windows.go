//go:build windows

package certstore

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ncrypt             = windows.NewLazySystemDLL("ncrypt.dll")
	procNCryptSignHash = ncrypt.NewProc("NCryptSignHash")
)

const (
	ncryptSilentFlag = 0x00000040
	bcryptPadPKCS1   = 0x00000002
	bcryptPadPSS     = 0x00000008
)

const (
	nteSilentContext ncryptStatus = 0x80090022
	nteNotSupported  ncryptStatus = 0x80090029
)

type ncryptStatus uint32

func (s ncryptStatus) Error() string {
	switch s {
	case nteSilentContext:
		return "the key needs a PIN or consent prompt, and bd never shows one (NTE_SILENT_CONTEXT)"
	case nteNotSupported:
		return "the key provider does not support this operation (NTE_NOT_SUPPORTED)"
	}
	return fmt.Sprintf("NCrypt status 0x%08X: %v", uint32(s), windows.Errno(s))
}

type bcryptPKCS1PaddingInfo struct {
	algID *uint16
}

type bcryptPSSPaddingInfo struct {
	algID *uint16
	salt  uint32
}

var hashAlgIDs = map[crypto.Hash]*uint16{}

func init() {
	for h, name := range map[crypto.Hash]string{
		crypto.SHA1: "SHA1", crypto.SHA256: "SHA256", crypto.SHA384: "SHA384", crypto.SHA512: "SHA512",
	} {
		hashAlgIDs[h] = windows.StringToUTF16Ptr(name)
	}
}

// ncryptSignHash calls NCryptSignHash twice, for the size and then the
// signature. Tests replace it to simulate provider failures.
var ncryptSignHash = func(key uintptr, pad unsafe.Pointer, digest []byte, flags uintptr) ([]byte, error) {
	var size uint32
	// nolint:gosec // G103: pointer arguments converted inside the LazyProc.Call expression, which keeps them alive
	r, _, _ := procNCryptSignHash.Call(key, uintptr(pad), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		0, 0, uintptr(unsafe.Pointer(&size)), flags)
	if r != 0 {
		return nil, ncryptStatus(r)
	}
	sig := make([]byte, size)
	// nolint:gosec // G103: as above
	r, _, _ = procNCryptSignHash.Call(key, uintptr(pad), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		uintptr(unsafe.Pointer(&sig[0])), uintptr(size), uintptr(unsafe.Pointer(&size)), flags)
	if r != 0 {
		return nil, ncryptStatus(r)
	}
	return sig[:size], nil
}

// signer is a crypto.Signer whose private key stays in its Windows key
// storage provider.
//
// The key handle was acquired with CRYPT_ACQUIRE_CACHE_FLAG, so Windows owns
// it and releases it with the last reference to the certificate. ctx is the
// signer's own reference; release frees it once no signature is in progress.
type signer struct {
	pub       crypto.PublicKey
	pkcs1Only bool

	mu     sync.RWMutex
	ctx    *windows.CertContext
	key    uintptr
	closed bool

	// failed is set when a signature fails, for example after a smart card
	// was removed. The resolver then reopens the key at the next handshake
	// instead of reusing a handle that may be stale.
	failed atomic.Bool
}

// acquireSigner opens the private key of ctx and takes ownership of ctx.
// It frees ctx when it returns an error.
func acquireSigner(ctx *windows.CertContext, cert *x509.Certificate) (*signer, error) {
	var key windows.Handle
	var spec uint32
	var callerFree bool
	flags := uint32(windows.CRYPT_ACQUIRE_CACHE_FLAG | windows.CRYPT_ACQUIRE_SILENT_FLAG | windows.CRYPT_ACQUIRE_ONLY_NCRYPT_KEY_FLAG)
	if err := windows.CryptAcquireCertificatePrivateKey(ctx, flags, nil, &key, &spec, &callerFree); err != nil {
		_ = windows.CertFreeCertificateContext(ctx)
		var errno windows.Errno
		if errors.As(err, &errno) && ncryptStatus(errno) == nteSilentContext {
			return nil, fmt.Errorf("opening its private key: %w", nteSilentContext)
		}
		return nil, fmt.Errorf("opening its private key through CNG (keys reachable only through a legacy CryptoAPI provider are not supported): %w", err)
	}
	if spec != windows.CERT_NCRYPT_KEY_SPEC || callerFree {
		_ = windows.CertFreeCertificateContext(ctx)
		return nil, errors.New("opening its private key: Windows did not return a cached CNG key handle")
	}
	s := &signer{ctx: ctx, key: uintptr(key), pub: cert.PublicKey}
	if err := s.probe(); err != nil {
		s.release()
		return nil, err
	}
	return s, nil
}

// release frees the signer's certificate reference after any signature in
// progress finishes. Later signatures fail.
func (s *signer) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = windows.CertFreeCertificateContext(s.ctx)
	s.ctx, s.key = nil, 0
}

// probe makes and verifies a test signature, so a key that cannot sign fails
// when it is opened rather than in a handshake. An RSA key is probed with
// RSA-PSS, which TLS 1.3 requires: only NTE_NOT_SUPPORTED limits the key to
// PKCS#1 v1.5 and TLS 1.2, and a PKCS#1 v1.5 signature is then probed
// instead. Any other failure, including a key that needs a prompt, is an
// error.
func (s *signer) probe() error {
	digest := make([]byte, crypto.SHA256.Size())
	switch pub := s.pub.(type) {
	case *rsa.PublicKey:
		opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}
		sig, err := s.Sign(nil, digest, opts)
		if errors.Is(err, nteNotSupported) {
			s.pkcs1Only = true
			s.failed.Store(false)
			if sig, err = s.Sign(nil, digest, crypto.SHA256); err != nil {
				return fmt.Errorf("test signature failed: %w", err)
			}
			if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig); err != nil {
				return fmt.Errorf("test PKCS#1 v1.5 signature does not verify against the certificate's public key: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("test signature failed: %w", err)
		}
		if err := rsa.VerifyPSS(pub, crypto.SHA256, digest, sig, opts); err != nil {
			return fmt.Errorf("test RSA-PSS signature does not verify against the certificate's public key: %w", err)
		}
	case *ecdsa.PublicKey:
		sig, err := s.Sign(nil, digest, crypto.SHA256)
		if err != nil {
			return fmt.Errorf("test signature failed: %w", err)
		}
		if !ecdsa.VerifyASN1(pub, digest, sig) {
			return errors.New("test ECDSA signature does not verify against the certificate's public key")
		}
	}
	return nil
}

func (s *signer) Public() crypto.PublicKey { return s.pub }

func (s *signer) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if len(digest) == 0 || len(digest) != opts.HashFunc().Size() {
		return nil, fmt.Errorf("certstore: digest length %d does not match %v", len(digest), opts.HashFunc())
	}
	var pad unsafe.Pointer
	flags := uintptr(ncryptSilentFlag)
	switch s.pub.(type) {
	case *rsa.PublicKey:
		alg, ok := hashAlgIDs[opts.HashFunc()]
		if !ok {
			return nil, fmt.Errorf("certstore: unsupported hash %v for RSA", opts.HashFunc())
		}
		if pss, ok := opts.(*rsa.PSSOptions); ok {
			if s.pkcs1Only {
				return nil, errors.New("certstore: this key cannot make RSA-PSS signatures")
			}
			salt := pss.SaltLength
			switch {
			case salt == rsa.PSSSaltLengthEqualsHash || salt == rsa.PSSSaltLengthAuto:
				salt = opts.HashFunc().Size()
			case salt < 0:
				return nil, fmt.Errorf("certstore: unsupported RSA-PSS salt length %d", salt)
			}
			pad = unsafe.Pointer(&bcryptPSSPaddingInfo{algID: alg, salt: uint32(salt)}) // nolint:gosec // G103: padding info for NCryptSignHash
			flags |= bcryptPadPSS
		} else {
			pad = unsafe.Pointer(&bcryptPKCS1PaddingInfo{algID: alg}) // nolint:gosec // G103: padding info for NCryptSignHash
			flags |= bcryptPadPKCS1
		}
	case *ecdsa.PublicKey:
	default:
		return nil, fmt.Errorf("certstore: unsupported key type %T", s.pub)
	}

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, errors.New("certstore: the key handle was released")
	}
	sig, err := ncryptSignHash(s.key, pad, digest, flags)
	s.mu.RUnlock()
	runtime.KeepAlive(pad)
	if err != nil {
		s.failed.Store(true)
		return nil, fmt.Errorf("certstore: signing with the Windows key: %w", err)
	}
	if pub, ok := s.pub.(*ecdsa.PublicKey); ok {
		return ecdsaASN1(pub, sig)
	}
	return sig, nil
}

// ecdsaASN1 converts NCrypt's fixed-width r||s signature into the ASN.1 form
// crypto.Signer callers expect.
func ecdsaASN1(pub *ecdsa.PublicKey, raw []byte) ([]byte, error) {
	width := (pub.Curve.Params().BitSize + 7) / 8
	if len(raw) != 2*width {
		return nil, fmt.Errorf("certstore: ECDSA signature is %d bytes, want %d for %s", len(raw), 2*width, pub.Curve.Params().Name)
	}
	return asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:width]), new(big.Int).SetBytes(raw[width:])})
}
