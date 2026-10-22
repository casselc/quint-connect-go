// A writer appends entries to a durable journal. A write can fail, and the
// writer can crash and restart (at most once); it resumes from the durable
// count. Its faults are the environment's: connect.FaultScript imposes a
// trace's failures and crash on the program (faults_test.go).
module journal {
  var written: int
  var attempts: int
  var restarts: int

  action init = all { written' = 0, attempts' = 0, restarts' = 0 }

  action write = all {
    attempts < 6,
    written' = written + 1, attempts' = attempts + 1, restarts' = restarts,
  }

  action writeFail = all {
    attempts < 6,
    written' = written, attempts' = attempts + 1, restarts' = restarts,
  }

  action restart = all {
    restarts < 1, attempts < 6,
    written' = written, attempts' = attempts, restarts' = restarts + 1,
  }

  action step = any { write, writeFail, restart }

  val bounded = written <= attempts and attempts <= 6
}
