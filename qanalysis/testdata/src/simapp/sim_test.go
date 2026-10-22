package simapp

import (
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// Tests are checked, as by quintgo lint -sim: a System's Run closure
// lives in one.
var _ = qenv.Nominal{}
var t0 = time.Now() // want `time.Now reads the wall clock`
var t1 = time.Now() //qenv:ok the real cost
