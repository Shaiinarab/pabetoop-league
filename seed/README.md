# Seed data

`clubs.json` is a **placeholder list**, not real data. It contains 73 entries named
`باشگاه نمونه ۱` … `باشگاه نمونه ۷۳` — obviously synthetic, so nobody mistakes a demo
database for a real competition.

Replace it before you use the seed for anything real:

```bash
# point the generator at your own list
go run ./cmd/seed --clubs-file /path/to/your-clubs.json --db data/demo.db --force
```

## File format

A flat JSON array. One key, `name`, holding the club's official name:

```json
[
  { "name": "باشگاه نمونه ۱" },
  { "name": "باشگاه نمونه ۲" }
]
```

Rules the generator relies on:

- **At least 12 entries.** The premier competition takes 12 teams; the command exits
  with a Persian error if the list is shorter.
- **Names are authoritative and never rewritten** (decision D7). The seed stores exactly
  the bytes you give it — no trimming, no normalisation, no de-duplication. Two entries
  that differ only by ZWNJ or an Arabic-vs-Persian letter are two different clubs, and
  that is deliberate.
- **Order is not preserved.** Clubs are read and stored in the order given, but the UI
  sorts them Persian-alphabetically.

## What the seed creates

`go run ./cmd/seed --db data/demo.db` builds one complete, deterministic season:

- season `۱۴۰۵–۱۴۰۶`, activated;
- the five youth age categories (10–14) and, per age, a Premier competition (12 teams)
  plus League 1 groups of 10 (U10: A,B · U11: A,B,C · U12: A,B,C,D · U13: A,B,C · U14: A,B);
- double round-robin fixtures per competition, balanced home/away;
- deterministic results: about 55% of weeks finished, with forced edge cases (one 5-0,
  two 0-0 draws, and a deliberately engineered tie-break in the U12 Premier).

It is **reproducible**: the same `--clubs-file` and the same generator seed always
produce byte-identical content, so demo screenshots and test expectations stay stable.

## What the seed must never contain

Real member, guardian or player data. Clubs are public organisations; families are not.
If you need to demo the product with names that are not the placeholders, use a
fictional list you invent for the purpose.
