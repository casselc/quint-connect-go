// Package qobs is a stub of the real package for analysistest.
package qobs

import "context"

// Record records a step.
func Record(ctx context.Context, action string, err error, kv ...any) {}

// Recorder records steps.
type Recorder struct{}

// Record records a step.
func (r *Recorder) Record(ctx context.Context, action string, err error, kv ...any) {}
