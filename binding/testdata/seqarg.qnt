// A model whose action has a parameter named like a step field (seq).
module seqarg {
  var applied: List[int]
  action init = applied' = []
  action apply(seq: int): bool = applied' = applied.append(seq)
  action step = { nondet n = 1.to(3).oneOf()  apply(n) }
}
