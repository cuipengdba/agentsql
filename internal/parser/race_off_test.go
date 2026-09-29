//go:build !race

package parser

// raceEnabled reports whether the test binary was built with the race detector.
const raceEnabled = false
