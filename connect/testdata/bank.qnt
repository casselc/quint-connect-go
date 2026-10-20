// A small bank for connect's own tests: accounts, deposits, withdrawals and
// transfers. Every nondet binding is named after the parameter it feeds, so
// quint run --mbt picks line up with action parameters.
module bank {
  pure val ACCOUNTS = Set("alice", "bob")
  pure val MAX = 5

  var balances: str -> int
  var ops: int

  action init = all { balances' = ACCOUNTS.mapBy(_ => 0), ops' = 0 }

  action deposit(account: str, amount: int): bool = all {
    amount > 0,
    balances' = balances.setBy(account, b => b + amount),
    ops' = ops + 1,
  }

  action withdraw(account: str, amount: int): bool = all {
    amount > 0,
    balances.get(account) >= amount,
    balances' = balances.setBy(account, b => b - amount),
    ops' = ops + 1,
  }

  action transfer(src: str, dst: str, amount: int): bool = all {
    src != dst,
    amount > 0,
    balances.get(src) >= amount,
    balances' = balances.setBy(src, b => b - amount).setBy(dst, b => b + amount),
    ops' = ops + 1,
  }

  action step = {
    nondet account = ACCOUNTS.oneOf()
    nondet src = ACCOUNTS.oneOf()
    nondet dst = ACCOUNTS.oneOf()
    nondet amount = 1.to(MAX).oneOf()
    any { deposit(account, amount), withdraw(account, amount), transfer(src, dst, amount) }
  }

  val nonNegative = ACCOUNTS.forall(a => balances.get(a) >= 0)

  run aliceSavesTest = init.then(deposit("alice", 3)).then(withdraw("alice", 1)).then(transfer("alice", "bob", 2))
}
