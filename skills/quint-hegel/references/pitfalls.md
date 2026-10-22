# Pitfalls


- Hegel runs cases sequentially. With `hegel.WithConcurrency`, the rules
  of one case share its recorder. Record under the lock that orders the
  effect, so the recording order is the effect order.
- A test case that records nothing is vacuously fine: `AllowEmpty`, and
  the checker treats it as OK.
- `hegel-go` needs Go ≥ 1.26.1. Its engine is embedded (no Python, no
  server), and runs offline after the first download.
- For distributed systems, embed `hegelnet.Network` (examples/glomers)
  in the machine. Hegel then schedules deliveries, losses, partitions
  and timers next to the client requests. Settle at the end of each case
  (heal, quiesce, record `settle`), so properties conditioned on
  quiescence are checked.
