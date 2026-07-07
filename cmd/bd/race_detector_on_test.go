//go:build race

package main

// raceEnabled reports whether the test binary was built with the race
// detector (-race). Wall-clock performance budgets are unreliable under
// race instrumentation (it adds multi-x overhead), so timing-sensitive
// benches consult this flag before enforcing a duration bound.
const raceEnabled = true
