// -*- mode: Bluespec; -*-
// A ledger of three accounts that moves money by idempotent transfers.
//
// The model is the requirement a client relies on, not the server's
// design: a transfer applies once per key, only with the funds there, and
// money is neither created nor lost. Lost responses, retries, duplicate
// requests and crashes do not appear: they show up as steps these actions
// do not allow (a key applied twice, an overdraft) or as balances that
// differ from the model's (a lost update).
module ledger {
  const INITIAL: int

  pure val ACCOUNTS = Set("a", "b", "c")

  type Transfer = { src: str, dest: str, amount: int }

  var balance: str -> int
  var applied: str -> Transfer // idempotency key -> the transfer it applied

  action init = all {
    balance' = ACCOUNTS.mapBy(_ => INITIAL),
    applied' = Map(),
  }

  pure def valid(src: str, dest: str, amount: int): bool =
    ACCOUNTS.contains(src) and ACCOUNTS.contains(dest) and src != dest and amount > 0

  // A transfer moves the money, once per key, only if it is there.
  action transfer(key: str, src: str, dest: str, amount: int): bool = all {
    valid(src, dest, amount),
    not(applied.keys().contains(key)),
    balance.get(src) >= amount,
    balance' = balance.set(src, balance.get(src) - amount).set(dest, balance.get(dest) + amount),
    applied' = applied.put(key, { src: src, dest: dest, amount: amount }),
  }

  // A refusal changes nothing, and is allowed only when the funds are not
  // there: a ledger that refuses a transfer it could make is wrong too.
  action refuse(key: str, src: str, dest: str, amount: int): bool = all {
    valid(src, dest, amount),
    not(applied.keys().contains(key)),
    balance.get(src) < amount,
    balance' = balance, applied' = applied,
  }

  // A request with an applied key is answered from the first, unchanged,
  // and must be the same transfer.
  action replay(key: str, src: str, dest: str, amount: int): bool = all {
    applied.keys().contains(key),
    applied.get(key) == { src: src, dest: dest, amount: amount },
    balance' = balance, applied' = applied,
  }

  // A read changes nothing (the binding compares what it saw).
  action read(account: str): bool = all {
    ACCOUNTS.contains(account),
    balance' = balance, applied' = applied,
  }

  val conservation = ACCOUNTS.fold(0, (s, a) => s + balance.get(a)) == ACCOUNTS.size() * INITIAL
  val nonNegative = ACCOUNTS.forall(a => balance.get(a) >= 0)

  // For quint run: random clients with keys k0..k5.
  action step = {
    nondet key = Set("k0", "k1", "k2", "k3", "k4", "k5").oneOf()
    nondet src = ACCOUNTS.oneOf()
    nondet dest = ACCOUNTS.oneOf()
    nondet amount = 1.to(INITIAL).oneOf()
    nondet account = ACCOUNTS.oneOf()
    any {
      transfer(key, src, dest, amount),
      refuse(key, src, dest, amount),
      replay(key, src, dest, amount),
      read(account),
    }
  }
}

module ledger5 {
  import ledger(INITIAL = 5).*
}
