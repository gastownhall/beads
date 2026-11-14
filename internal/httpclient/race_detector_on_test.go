//go:build cgo && race

package httpclient

// raceEnabled reports whether the test binary was built with the race
// detector (-race), as the embedded lane's httpclient_served_test is. The
// served tier stands its `bd serve` handler up over the in-process embedded
// Dolt engine, whose own goroutine/lock machinery the detector instruments
// (see internal/storage/embeddeddolt/race_detector_on_test.go), so a large
// write that takes seconds without it can outlast the client's per-request
// timeout with it. The pair carries `cgo` like that package's: its consumers
// are served_*_test.go files, which build only under cgo.
const raceEnabled = true
