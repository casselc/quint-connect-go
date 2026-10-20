// -*- mode: Bluespec; -*-
// The bank of examples/bank: accounts that can be credited, debited and
// transferred between, and must never be overdrawn.
//
// Conventions that make the model usable from Go (see the repository's
// docs/modeling.md):
//   - every action the implementation performs is a named action whose
//     parameters can be observed in the implementation;
//   - `step` picks each parameter with a `nondet` named exactly like the
//     parameter it feeds, so `quint run --mbt` picks line up with them;
//   - properties are boolean `val`s, checked by `quint run --invariant`
//     on the design and by quintgo on recorded runs of the code.
module bank {
  const ACCOUNTS: Set[str]
  const MAX_AMOUNT: int

  var balances: str -> int

  action init = balances' = ACCOUNTS.mapBy(_ => 0)

  action deposit(account: str, amount: int): bool = all {
    ACCOUNTS.contains(account),
    amount > 0,
    balances' = balances.setBy(account, b => b + amount),
  }

  action withdraw(account: str, amount: int): bool = all {
    ACCOUNTS.contains(account),
    amount > 0,
    balances.get(account) >= amount,
    balances' = balances.setBy(account, b => b - amount),
  }

  action transfer(src: str, dst: str, amount: int): bool = all {
    ACCOUNTS.contains(src),
    ACCOUNTS.contains(dst),
    src != dst,
    amount > 0,
    balances.get(src) >= amount,
    balances' = balances.setBy(src, b => b - amount).setBy(dst, b => b + amount),
  }

  action step = {
    nondet account = ACCOUNTS.oneOf()
    nondet src = ACCOUNTS.oneOf()
    nondet dst = ACCOUNTS.oneOf()
    nondet amount = 1.to(MAX_AMOUNT).oneOf()
    any {
      deposit(account, amount),
      withdraw(account, amount),
      transfer(src, dst, amount),
    }
  }

  // Properties.
  val noOverdraft = ACCOUNTS.forall(a => balances.get(a) >= 0)

  // A named scenario, for `quint test` and as a regression of the code.
  run payRentTest =
    init
      .then(deposit("alice", 5))
      .then(transfer("alice", "bob", 3))
      .then(withdraw("bob", 2))
      .expect(balances.get("alice") == 2 and balances.get("bob") == 1)
}

// The design as deployed: three accounts, amounts up to 5.
module bankDesign {
  import bank(ACCOUNTS = Set("alice", "bob", "carol"), MAX_AMOUNT = 5).*
}
