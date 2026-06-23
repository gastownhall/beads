# Pure-Go zstd Seam Evidence

Bead: `mybd-hli9`
Upstream issue: `gastownhall/beads#4249`
Original draft: `gastownhall/beads#4408`

This branch is the narrow compression proof only. It replaces
`github.com/dolthub/gozstd` with this local module so maintainers can inspect
whether Dolt's NBS archive zstd surface can be satisfied by a pure-Go
implementation over `github.com/klauspost/compress/zstd`.

It intentionally does not change Beads' `cgo` build tags. The broad Beads
`cgo` to `nocgo` migration is kept in a separate stacked branch so the storage
seam evidence and the mechanical build-selection sweep can be reviewed
independently.

The shim implements the small `gozstd` API surface used by Dolt's NBS archive
path: plain compression/decompression, dictionary compression/decompression,
dictionary construction, and dictionary handle release. Dictionary decode uses
`WithDecoderDicts`, with raw-dictionary fallback for experiment data written by
earlier shim iterations.

## Verified Earlier

From the original experiment, with `CGO_ENABLED=0 -tags gms_pure_go`:

```powershell
go test github.com/dolthub/dolt/go/store/nbs -run 'TestArchive(SingleZStdChunk|DictDecompression|MixedCompression|ConjoinAll|ConjoinAllComprehensive)$'
```

The targeted Dolt NBS archive tests passed with the shim. A separate standalone
legacy-decode check also verified that klauspost can decode frames written by
the real cgo libzstd path with ZDICT-trained dictionaries, so existing archive
data is expected to remain readable.

## What This Branch Does Not Prove

By itself, this branch does not make a `CGO_ENABLED=0` Beads binary include
embedded Dolt, because Beads still gates embedded-source files on the Go `cgo`
build tag. That is the job of the separate build-tag migration branch.

The durable implementation should live in Dolt's `store/nbs` package as a
build-tagged seam. A Beads-local `replace` is useful evidence, but it is not a
good long-term product shape because it makes Beads override a transitive
storage dependency and breaks `go install ...@latest`.
