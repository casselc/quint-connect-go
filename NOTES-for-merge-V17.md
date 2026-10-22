# Notes for merging V17 (refusals as steps in checkout)

Changed only in examples/checkout: model/checkout.qnt (action `refuse`,
in `step`), checkout.binding.yaml (`refuse` with `stock == {{.obs.stock}}`),
shop/shop.go (`PlaceOrder` records `refuse` at the commit of its read-only
transaction, returns `ErrSoldOut` after it), README.md (the model bullet).
No test claim changed: TestMutants' claims hold as they were.

## docs/DESIGN.md §26: replace the last survivor bullet and add a paragraph

Replace the bullet starting "- `PlaceOrder`'s stock check (`stock < qty`, its
boundary) and error branches: ..." with:

```
- `PlaceOrder`'s error branches (`err != nil`, `ok` negated): a shop that
  places nothing records nothing, and a safety model accepts a run with no
  steps;
- the refusal's own recording (two mutants dropping it) and the
  `ErrSoldOut` return negated: a missing refusal is a step the model
  cannot require (a client may give up before any), and the client's
  answer is no step.
```

Then, after the bullets and before "The report is a list to read":

```
Refusals are steps (V17). A refused order recorded nothing, so the stock
check was unconstrained both ways. The model has `refuse(order, qty)`,
enabled only when `stock < qty`, and changes nothing. The shop records it
from its read-only transaction's commit: returning ErrSoldOut from the
transaction would roll back and drop the hook, so it returns nil and the
error after. The binding compares the stock. Both stock-check mutants
(negated, `<=` for `<`) are now killed: 70 reached, 51 killed (73%), 19
survive, 12 unreached. The three new mutants, in the refusal code, survive:
they are among the bullets above.
```

Also in §26's first line of results, "67 mutants reached" stays as the
history; the new numbers are in the paragraph above.

And DESIGN line ~2530 (grpcledger, "Refusals are steps."): replace
"(Checkout's open question, refused orders that record no step, is the
same design point.)" with "(Checkout's refused orders are steps too, V17.)"

## docs/ROADMAP.md

In Status, add:

```
- V17: done (DESIGN §26): checkout's refusals are steps (`refuse`, enabled
  when stock < qty); the stock check's two mutants killed, 51 of 70 (73%).
```

## AGENTS.md / skills

Suggested gotcha (AGENTS.md), next to the OnCommit ones:

```
- A step for a refusal recorded with `Txn.OnCommit` needs the transaction
  to commit: return nil from it and the error after `qenv.Retry`
  (checkout's `PlaceOrder`). An error rolls back and drops the hook.
```

Suggested addition to skills/quint-modeling-patterns/SKILL.md §7, after
the Refusals bullet:

```
  A skipped refusal leaves the check that refuses unconstrained: a call
  wrongly *refused* is no step either. When that matters, give the refusal
  an action enabled only when the call must fail (checkout's
  `refuse(order, qty)`, when `stock < qty`): its mutants are then killed.
```
