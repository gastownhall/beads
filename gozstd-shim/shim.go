// Package gozstd is a pure-Go replacement for the subset of
// github.com/dolthub/gozstd used by Dolt's NBS archive path.
package gozstd

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

// DefaultCompressionLevel mirrors gozstd's ZSTD_CLEVEL_DEFAULT.
const DefaultCompressionLevel = 3

var (
	plainEnc *zstd.Encoder
	plainDec *zstd.Decoder
	initOnce sync.Once
)

func initPlain() {
	initOnce.Do(func() {
		plainEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		plainDec, _ = zstd.NewReader(nil)
	})
}

// Compress appends the zstd-compressed form of src to dst.
func Compress(dst, src []byte) []byte {
	initPlain()
	return plainEnc.EncodeAll(src, dst)
}

// Decompress appends the decompressed form of src to dst.
func Decompress(dst, src []byte) ([]byte, error) {
	initPlain()
	return plainDec.DecodeAll(src, dst)
}

// BuildDict trains a zstd dictionary from samples, matching gozstd's API.
func BuildDict(samples [][]byte, desiredDictLen int) []byte {
	if len(samples) == 0 || desiredDictLen <= 0 {
		return nil
	}
	history := make([]byte, 0, desiredDictLen)
	for _, sample := range samples {
		if len(history) >= desiredDictLen {
			break
		}
		if remaining := desiredDictLen - len(history); len(sample) > remaining {
			sample = sample[:remaining]
		}
		history = append(history, sample...)
	}
	if len(history) < 8 {
		return nil
	}
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		Contents: samples,
		History:  history,
		Level:    zstd.SpeedDefault,
	})
	if err != nil || len(dict) == 0 {
		return history
	}
	return dict
}

// CDict is a compression dictionary handle.
type CDict struct {
	enc *zstd.Encoder
}

// NewCDict creates a compression dictionary from zstd dictionary bytes.
func NewCDict(dict []byte) (*CDict, error) {
	opts := []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedDefault)}
	if len(dict) > 0 {
		opts = append(opts, zstd.WithEncoderDict(dict))
	}
	enc, err := zstd.NewWriter(nil, opts...)
	if err != nil && len(dict) > 0 {
		opts = []zstd.EOption{
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderDictRaw(1, dict),
		}
		enc, err = zstd.NewWriter(nil, opts...)
	}
	if err != nil {
		return nil, err
	}
	return &CDict{enc: enc}, nil
}

// Release frees the dictionary's resources.
func (cd *CDict) Release() {
	if cd != nil && cd.enc != nil {
		cd.enc.Close()
		cd.enc = nil
	}
}

// DDict is a decompression dictionary handle.
type DDict struct {
	dec *zstd.Decoder
}

// NewDDict creates a decompression dictionary from zstd dictionary bytes.
func NewDDict(dict []byte) (*DDict, error) {
	var opts []zstd.DOption
	if len(dict) > 0 {
		opts = append(opts, zstd.WithDecoderDicts(dict))
	}
	dec, err := zstd.NewReader(nil, opts...)
	if err != nil && len(dict) > 0 {
		opts = []zstd.DOption{zstd.WithDecoderDictRaw(1, dict)}
		dec, err = zstd.NewReader(nil, opts...)
	}
	if err != nil {
		return nil, err
	}
	return &DDict{dec: dec}, nil
}

// Release frees the dictionary's resources.
func (dd *DDict) Release() {
	if dd != nil && dd.dec != nil {
		dd.dec.Close()
		dd.dec = nil
	}
}

// CompressDict appends the dictionary-compressed form of src to dst.
func CompressDict(dst, src []byte, cd *CDict) []byte {
	return cd.enc.EncodeAll(src, dst)
}

// DecompressDict appends the dictionary-decompressed form of src to dst.
func DecompressDict(dst, src []byte, dd *DDict) ([]byte, error) {
	return dd.dec.DecodeAll(src, dst)
}
