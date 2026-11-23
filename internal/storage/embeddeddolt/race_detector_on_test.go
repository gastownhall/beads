//go:build cgo && race

package embeddeddolt_test

// This pair uses `cgo && race` / `cgo && !race`, unlike the plain
// `race`/`!race` pair in internal/workapi and cmd/bd: every consumer of
// raceEnabled in this package (embeddeddolt_test) is itself gated `//go:build
// cgo`, since the embedded backend only builds under cgo at all. Adding the
// matching cgo tag here keeps this file out of non-cgo builds too, where
// raceEnabled would otherwise compile with no consumer and trip an unused
// lint finding in that lane instead of simply being absent.
//
// raceEnabled reports whether the test binary was built with the race
// detector (-race). Wall-clock numbers are unreliable (and, for the embedded
// backend specifically, dramatically inflated) under race instrumentation:
// the in-process Dolt engine's internal goroutine/lock machinery runs
// entirely inside this test binary, so every synchronization point it hits
// is itself instrumented, unlike the Dolt server (TCP) tier where that
// machinery runs in a separate, non-raced subprocess. Timing-sensitive
// measurements consult this flag before running their most expensive case.
// This is a sibling of the pair in internal/workapi and cmd/bd, which solve
// the same problem for their own packages.
const raceEnabled = true
