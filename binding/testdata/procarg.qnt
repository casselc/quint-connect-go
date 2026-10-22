// A model whose action has a parameter named like the step's own .process.
module procarg {
  var crashed: Set[str]
  action init = crashed' = Set()
  action crash(process: str): bool = crashed' = crashed.union(Set(process))
  action step = { nondet p = Set("a", "b").oneOf()  crash(p) }
}
