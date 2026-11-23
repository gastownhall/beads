//go:build cgo && !race

package embeddeddolt_test

// raceEnabled is false in non-race builds; see race_detector_on_test.go.
const raceEnabled = false
