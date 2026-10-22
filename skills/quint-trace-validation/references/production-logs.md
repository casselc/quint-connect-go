# Production logs: select and sanitize first


```sh
QUINTGO_SALT=$SECRET quintgo steps -actors 'checkout-*' -since 2026-10-01T00:00:00Z \
  -pseudonymize account,customer -binding app.binding.yaml -out clean.jsonl spans.jsonl
quintgo validate -noitf -binding app.binding.yaml clean.jsonl
```

- `-binding` keeps only the fields the binding reads, and drops error
  texts: what a check needs, nothing more.
- Pseudonyms keep equality, so interned ids (`intern:`) still work. A
  model constant naming concrete ids (`ACCOUNTS: Set("alice", ...)`) must
  name the pseudonyms instead (`qtrace.Filter.Pseudonym`), or intern the
  field.

