// Package simdeps holds a test, no code: the packages that must run inside
// a simulation (gosim translates everything they import) depend on no
// native code, so neither on Hegel (its library is loaded through purego).
// Hegel adapters live in quinthegel, and in hegelnet for the Glomers
// examples.
package simdeps
