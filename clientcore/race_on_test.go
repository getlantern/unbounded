//go:build race

package clientcore

// raceEnabled reports whether the race detector is on, which slows pion enough that a data
// channel is bound by CPU rather than by its network.
const raceEnabled = true
