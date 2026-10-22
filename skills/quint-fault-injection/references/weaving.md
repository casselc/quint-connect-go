# Code you cannot rewrite against seams: weave it

If the code uses `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup` or `go`
statements and you want it unchanged (a stand-in that must track the
real code, or the real code), weave it:
- add `_ "github.com/casselc/quint-connect-go/qenv/weave"` to the
  project's `orchestrion.tool.go`. Then test with qweave (`qweave go test
  ./...`, from the qweave module in this repository), or with
  Orchestrion (`go run github.com/DataDog/orchestrion go test ./...`);
- mark each go statement `//qenv:go` (its call must be a function
  literal). Under qweave that is all: it weaves lock method values
  (`return mu.RUnlock`) and calls promoted through an embedded lock too.
  Under Orchestrion, also turn method values into calls
  (`func() { mu.RUnlock() }`) and name embedded locks
  (`mu sync.Mutex`). `quintgo lint -weave DIR` lists what Orchestrion
  misses;
- code started in a woven goroutine uses `context.Background()` (or the
  goroutine's own context), not one captured from the task that started
  it: qenv resolves a context-less call to the goroutine's own task, and
  another task's context makes it sleep and wait as that task;
- pass the code its clock if it takes one (`Now: s.Now`);
- skip simulation tests in unwoven builds. A `//quint:action` probe that
  records only when woven tells you which build you are in
  (examples/edgepublish/sim `Woven`).

Inside a simulation the woven locks are scheduling points, so seeds and
Hegel explore their interleavings. Outside one, the real methods run.
