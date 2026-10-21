// -*- mode: Bluespec; -*-
// A checkout saga: orders reserve stock; the payment provider charges;
// paid orders ship; an order may be cancelled until it ships, returning its
// stock and (once all is quiet) its money.
//
// The model is the requirement, not the implementation: the provider may
// charge any placed order at any time (retries, duplicates, a charge racing
// a cancellation), and the properties say what must hold anyway. No
// database, broker or network appears here: their anomalies show up as
// steps of these actions in an order (or a number) the model forbids.
module checkout {
  const STOCK: int

  var stock: int
  var orders: str -> int     // placed order -> quantity
  var paid: Set[str]
  var shipped: Set[str]
  var cancelled: Set[str]
  var charges: str -> int    // at the provider, per order
  var refunds: str -> int
  var doubleCharge: bool     // ghost: some order was charged twice
  var settled: bool          // the system has quiesced

  pure def getOr(m: str -> int, k: str): int = if (m.keys().contains(k)) m.get(k) else 0

  action init = all {
    stock' = STOCK, orders' = Map(), paid' = Set(), shipped' = Set(), cancelled' = Set(),
    charges' = Map(), refunds' = Map(), doubleCharge' = false, settled' = false,
  }

  // A new order takes stock that is there: overselling is not a step.
  action place(order: str, qty: int): bool = all {
    not(orders.keys().contains(order)), qty > 0, stock >= qty,
    stock' = stock - qty, orders' = orders.put(order, qty),
    paid' = paid, shipped' = shipped, cancelled' = cancelled, charges' = charges, refunds' = refunds,
    doubleCharge' = doubleCharge, settled' = settled,
  }

  // The provider charges a placed order. A second charge is allowed (it
  // happens) but remembered: chargedAtMostOnce is the property.
  action charge(order: str): bool = all {
    orders.keys().contains(order),
    doubleCharge' = (doubleCharge or getOr(charges, order) > 0),
    charges' = charges.put(order, getOr(charges, order) + 1),
    stock' = stock, orders' = orders, paid' = paid, shipped' = shipped, cancelled' = cancelled,
    refunds' = refunds, settled' = settled,
  }

  // The provider refunds only what it charged.
  action refund(order: str): bool = all {
    getOr(refunds, order) < getOr(charges, order),
    refunds' = refunds.put(order, getOr(refunds, order) + 1),
    stock' = stock, orders' = orders, paid' = paid, shipped' = shipped, cancelled' = cancelled,
    charges' = charges, doubleCharge' = doubleCharge, settled' = settled,
  }

  // The shop learns an order is paid only after a charge.
  action markPaid(order: str): bool = all {
    getOr(charges, order) > 0, not(paid.contains(order)),
    paid' = paid.union(Set(order)),
    stock' = stock, orders' = orders, shipped' = shipped, cancelled' = cancelled, charges' = charges,
    refunds' = refunds, doubleCharge' = doubleCharge, settled' = settled,
  }

  // Only paid, uncancelled orders ship, once.
  action ship(order: str): bool = all {
    paid.contains(order), not(shipped.contains(order)), not(cancelled.contains(order)),
    shipped' = shipped.union(Set(order)),
    stock' = stock, orders' = orders, paid' = paid, cancelled' = cancelled, charges' = charges,
    refunds' = refunds, doubleCharge' = doubleCharge, settled' = settled,
  }

  // An order can be cancelled until it ships; its stock comes back.
  action cancel(order: str): bool = all {
    orders.keys().contains(order), not(shipped.contains(order)), not(cancelled.contains(order)),
    cancelled' = cancelled.union(Set(order)), stock' = stock + orders.get(order),
    orders' = orders, paid' = paid, shipped' = shipped, charges' = charges,
    refunds' = refunds, doubleCharge' = doubleCharge, settled' = settled,
  }

  action settle = all {
    settled' = true,
    stock' = stock, orders' = orders, paid' = paid, shipped' = shipped, cancelled' = cancelled,
    charges' = charges, refunds' = refunds, doubleCharge' = doubleCharge,
  }

  val chargedAtMostOnce = not(doubleCharge)

  // Once quiet, every order has shipped or been cancelled, and the provider
  // kept exactly the money of the shipped ones.
  val settledCorrectly = settled implies orders.keys().forall(o =>
    (shipped.contains(o) or cancelled.contains(o)) and
    getOr(charges, o) - getOr(refunds, o) == (if (cancelled.contains(o)) 0 else 1))

  // For simulation: the provider and shop in any order, from a few orders.
  action step = {
    nondet order = Set("o1", "o2", "o3").oneOf()
    nondet qty = 1.to(2).oneOf()
    any { place(order, qty), charge(order), refund(order), markPaid(order), ship(order), cancel(order) }
  }
}

module checkoutDesign {
  import checkout(STOCK = 2).*
}
