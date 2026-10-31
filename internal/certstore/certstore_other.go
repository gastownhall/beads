//go:build !windows

package certstore

import (
	"crypto/tls"
	"io"
)

// Configure is unavailable outside Windows: it validates the selector and
// returns ErrUnsupported.
func (s Selector) Configure(_ *tls.Config) (io.Closer, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
