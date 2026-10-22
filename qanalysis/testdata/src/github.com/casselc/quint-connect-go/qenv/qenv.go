// Package qenv is a stub of the real package for analysistest (GOPATH
// mode): the analyzers look at import paths, not the package's code.
package qenv

import "context"

// Sim is a simulation.
type Sim struct{}

// Context is the simulation's context.
func (*Sim) Context() context.Context { return context.Background() }

// Nominal is a chooser.
type Nominal struct{}
