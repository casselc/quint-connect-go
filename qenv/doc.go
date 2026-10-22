// Package qenv simulates the environment a system runs in (scheduling,
// time, crashes, stores, queues, remote calls) deterministically, with every
// nondeterministic choice made by one Chooser. A fault is a choice the
// environment's contract allows: a lost response, a redelivered message, a
// read from an older snapshot. Option 0 of every choice is the nominal one
// (no fault, first in line), so a Hegel-driven Chooser shrinks a failing
// run toward fewer faults and a canonical schedule.
//
// The same run can be driven by Hegel (quinthegel.Choices), a seed
// (NewSeeded), a recorded list of choices (Replay), or no faults at all
// (Nominal). The choosers are implemented apart from the simulation
// (qenv/internal/choice) and are qenv's API.
package qenv
