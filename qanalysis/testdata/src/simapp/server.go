package simapp

import "time"

// No qenv import: not simulation code.
func serve() { go func() {}(); _ = time.Now() }
