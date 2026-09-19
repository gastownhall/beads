//go:build race

package workapi

// raceEnabled reports whether the test binary was built with the race
// detector (-race). Wall-clock performance budgets are unreliable under race
// instrumentation (it adds multi-x overhead), so timing-sensitive benches
// consult this flag before enforcing a duration bound. It is a sibling of the
// pair in cmd/bd, which is where the sweep's large-fixture budget used to
// live.
const raceEnabled = true
