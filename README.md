# WARREN

Coordinated abuse-ring detection for payments.

A rabbit warren looks like separate burrows from the surface. WARREN finds the
tunnels: sets of accounts that transact as if unrelated but are worked by one
coordinated operation — the loss class per-transaction scoring is structurally
blind to, because a ring is designed so that each individual transfer looks
ordinary.

**The unit of defence is the ring, not the payment.**

WARREN detects at the level of the group, explains what it found, decides inside
limits it cannot exceed, acts on the accounts within those limits, and records
both the decision and the action tamper-evidently.

---

## What it does, in one pass

1. **Filter** to ACH — laundering here is 86.6% ACH against 11.75% of ordinary
   traffic, so one filter drops 88% of the ledger while keeping 86.6% of the
   laundering.
2. **Window** time into overlapping slices. Windowing is load-bearing, not an
   optimisation: some accounts sit in dozens of distinct rings, and over the
   whole ledger those hubs chain unrelated rings into one blob.
3. **Connect** accounts transacting together, via union-find, into candidate
   groups.
4. **Rank** with a hand-written logistic regression over twelve candidate-level
   features.
5. **Assess** the top candidates with a model, then clamp the result in code.
6. **Act** — a policy-approved block leases a freeze over the ring's accounts;
   a hold leases a watch, which is visible, expires, and stops nothing.
7. **Record** every decision and every action in two cross-referenced
   hash-chained logs.

Detection and ranking are deliberately separate. The graph pass is built for
recall — 74% of labelled rings, but 95k candidates at 0.2% purity — and ranking
is what makes it workable. Candidate-level ranking is also a far healthier
learning problem than transaction-level: roughly one candidate in fifty bears a
ring, against one transfer in a thousand.

## Measured results

Held out, temporal split, never random. Windows overlap, so shuffling rows would
put near-duplicates of the same ring on both sides of the split.

| alerts | rings found | precision | recall | value held |
|-------:|------------:|----------:|-------:|-----------:|
| 50 | 39/182 | 14.32% | 22.62% | 3.13bn |
| 250 | 76/182 | 7.29% | 46.65% | 31.04bn |
| 1,000 | 108/182 | 3.55% | 59.63% | 74.91bn |

14.32% at 50 alerts is **~22× lift** against the **0.64%** laundering rate of the
ACH-filtered, active-period ledger the ranker actually scores. That is the figure
to quote. Against the **0.1019%** rate of the *raw* 5.08m-row ledger the same
precision reads as ~140×, but that denominator credits the ranker with the
channel filter's work: filtering to ACH drops 88% of the ledger while keeping
86.6% of the laundering, and most of the gap between the two multiples is that
step rather than the ranking. State the population the multiple is measured
against, or the number means nothing.

Recall is reported per ring shape, because one average hides a shape the
detector never finds. At 1,000 alerts: CYCLE 70.8, SCATTER-GATHER 70.8, STACK
68.4, GATHER-SCATTER 67.6, FAN-IN 61.9, RANDOM 50.0, FAN-OUT 43.5,
**BIPARTITE 25.0**. The last is genuinely weak and is reported so it cannot hide.

**Latency** is published three ways, because the usual `<100ms` bar is a
per-transaction bar and WARREN decides over a window. Scoring a candidate takes
190ns at p50 against a measured 20ns timer floor; detection costs ~840ns per
transfer; arrival to decision is 12.1h at p50, bounded by the stride. Only the
first clears the bar, and the console says so rather than quoting the flattering
number alone.

**The enforcement ceiling is the headline, not the interception.** An oracle
detector told the true ring membership, acting at the earliest instant any
windowed detector could see the ring, could stop only **10.76% of ring value at
72h windows** and 31.75% at 24h. Nine tenths of a ring's money has already moved
before anyone can see the ring at all. Window width is decision latency is the
enforcement ceiling — the latency numbers and this one are the same number
twice. WARREN reaches 0.66% of that ceiling, which is small and stated as small.

Everything above, and how it was produced, is in
**[docs/FINDINGS.md](docs/FINDINGS.md)**. Every non-obvious choice, with the
alternative that was rejected and why, is in **[DECISIONS.md](DECISIONS.md)**.

## The part worth reading: what was thrown away

Five times a good-looking number turned out to be an artefact and was discarded.
This is the project's best material, and all of it is written up.

1. **Fallback account keys manufactured a signal.** Falling back to a raw card
   tuple where identifiers were missing pooled unrelated people into
   pseudo-accounts averaging 224 transactions, which linked into one 236-member
   "ring" holding nearly all the apparent enrichment. Unidentifiable
   transactions are now singletons: refusing to guess beats inventing an account.
2. **Unconstrained transitivity built a giant component.** One shared value per
   link chained 2,219 individually reasonable links into an 8,930-account
   "ring"; the next largest was 115. Links now need two independent shared
   values.
3. **The generator's quiet tail faked the metrics.** The simulator stops
   background traffic while laundering already in flight plays out, leaving a
   stretch that is 58–73% laundering against a 0.1% base rate. A 70/30 temporal
   split landed the whole test set inside it and reported precision 0.598,
   recall 1.000. The cut is now found from the volume collapse rather than
   hardcoded.
4. **An alert-budget confound.** A two-geometry configuration was compared at
   375 alerts *per pass* against single passes at 375 — spending 750 against
   375. Held to constant total budget the ordering inverted.
5. **Non-determinism reported as noise.** Two runs of the same command gave
   different answers, because candidate order followed Go's map iteration seed
   and with it the fitting set and every score tie.

**Rejected outright:** cross-account ring linking on IEEE-CIS. Measured 1.05×
enrichment against the linkable population — no signal. IEEE-CIS is anonymised and carries no email, phone, IP
or account id, so nothing ties separate accounts to one operator. The code is
kept, configurable, and claims nothing.

## The agent layer

**The model proposes, the policy disposes.** Model output is untrusted input:
schema-constrained to three actions, then clamped in code before it can affect
money. Authority does not live in the model.

The envelope binds *both* ways, which is the part usually missed:

- **Block** requires ranker score ≥ 0.90 **and** stated confidence ≥ 0.80
  **and** a total under the autonomous ceiling. Above the ceiling a person
  decides however certain the machine is.
- **Allow** is withheld at ranker score ≥ 0.50 — the model cannot wave through
  what the detector flagged.
- An **unrecognised action** is a malformed response and lands on review, never
  interpreted.
- The original proposal is preserved beside the final decision, so the
  disagreement is visible.

**Degradation chain:** a reasoning model, then a smaller one for when the first
is busy, then a deterministic rule that cannot fail and **never blocks** — an
autonomous block on a degraded path is exactly what should wait for a person.
`Chain.Assess` cannot return an error: a bounded decision always exists, because
the alternative is a decision made silently and without a record.

**Prompt safety:** only measured quantities reach the model. No name, reference
or memo field, so a party under investigation has no channel to address the
model judging them.

## Acting on it

A policy-approved block leases a **frozen** restriction over the ring's
accounts; later transfers out of a frozen account are stopped. A hold leases a
**watch**, which is visible, expires, and stops nothing. The gates already on
blocking are therefore the only gates on enforcement — there is no second,
looser path to freezing an account. Nothing is permanent, a single decision may
not freeze more than 25 accounts, and lifting is a first-class audited
operation.

**Two geometries, one role each.** A 72h/24h pass feeds the analyst queue and
never leases; a 24h/6h pass leases. Each has its own fitted ranker, because the
two see different candidate populations and a model fitted on one puts the
other's scores on a scale nothing measured.

**Two hash chains, cross-referenced.** `audit_log` records what the system
*decided*; `account_restrictions` records what it *did about it*, and every
lease names the entry that authorised it. Enforcement runs only after the
decision is committed, so a hold whose authority cannot be named is never
placed. `cmd/audit -tamper N` rewrites a decision in place without touching a
hash — the thing someone covering their tracks would do — and `-verify` then
names the accounts still held on the broken entry's authority:

```
CHAIN BROKEN at entry 5
  content hashes to 0ada0c91133c… but the row carries 2f52910b8b16…
  — this entry was edited after it was written

8 accounts are under a watch placed on the authority of entry 5 or later:
  134091|80CD76230   watch  ring 100002  expires 2026-09-10 20:00  (decision 5)
  ...
these restrictions no longer have a verifiable authority behind them.
```

## Data

**[IBM Anti-Money Laundering](https://www.kaggle.com/datasets/ealtman2019/ibm-transactions-for-anti-money-laundering-aml)**
(HI-Small) is the primary dataset: 5,078,345 transfers between 518,581 accounts
owned by 166,207 entities, 0.1019% laundering, and critically **370 labelled
rings across 8 named typologies**. It is synthetic — generated by a multi-agent
simulator — which is the trade accepted in exchange for ring-level ground truth.
Without labelled rings, nothing here could have been honestly measured.

**[IEEE-CIS Fraud Detection](https://www.kaggle.com/c/ieee-fraud-detection)** is
real card-not-present data: 590,540 transactions at 3.499% fraud. It is kept for
its account-level signal and as the record of a rejected hypothesis.

Both are gitignored under `data/`.

## Running it

Requires **Go 1.27+**, Docker, and a Kaggle account that has accepted the
competition rules.

```sh
docker compose up -d                       # Postgres 17 on :5432

python3 -m venv .venv && .venv/bin/pip install kaggle
for f in HI-Small_Trans.csv HI-Small_Patterns.txt HI-Small_accounts.csv; do
  .venv/bin/kaggle datasets download ealtman2019/ibm-transactions-for-anti-money-laundering-aml \
    -f "$f" -p data/aml/
done
go run ./cmd/aml-ingest -data data/aml -set HI-Small   # ~55s, streams via COPY

go run ./cmd/serve                         # the console, on :8080
```

The console is the way to see it working. It runs the pipeline once at startup
(about twelve seconds) and serves the ranked alert queue, the evidence and
decision for any single ring drawn as a graph, the model coefficients and
measured latencies, the audit trail with verify and tamper buttons, and the
holds currently in force with the enforcement queue behind them.

`go run ./cmd/serve -offline` runs it with **no model at all**, deciding on the
deterministic rule. It still decides, and it freezes nothing — the degraded path
never blocks. That is worth seeing.

### The other commands

```sh
go run ./cmd/detect                        # detection, ranking, held-out evaluation
go run ./cmd/detect -features base         # ...without the temporal features
go run ./cmd/detect -features anomaly      # ...with the isolation forest
go run ./cmd/detect -registry              # ...with a simulated I4C-style mule list
go run ./cmd/enforce -ceilings             # the interception ceiling, per geometry
go run ./cmd/compare                       # against a per-transaction baseline
go run ./cmd/assess -top 5                 # the pipeline through to decisions
go run ./cmd/audit -verify                 # check both chains
go run ./cmd/audit -tamper 4               # edit a decision in place, then verify
go run ./cmd/apitest -provider gemini      # is the provider reachable
```

`-features` exists so that every feature can be measured against its own
absence. A feature added with no way to run the pipeline without it is a feature
nobody can ever measure.

### Tests

```sh
go test ./...
```

The two hash chains are tested against real Postgres, in a throwaway schema per
run, because the property they claim — that concurrent writers cannot fork the
log — lives entirely in what Postgres does with a lock inside a transaction, and
a fake would reimplement that behaviour and then pass. They skip where no
database is reachable. Writing them found two real bugs on the first run.

## Layout

```
cmd/aml-ingest/    IBM AML   -> Postgres, including the ring labels
cmd/ingest/        IEEE-CIS  -> Postgres via the COPY protocol
cmd/rings/         entity resolution over IEEE-CIS, with a concentration report
cmd/detect/        detection, ranking, held-out evaluation, latency
cmd/enforce/       the action layer, replay, and the interception ceiling
cmd/compare/       head to head against the per-transaction opponent
cmd/assess/        the pipeline through to bounded, recorded decisions
cmd/serve/         the operator console
cmd/audit/         read, verify and tamper with the decision log
cmd/apitest/       provider reachability

internal/detect/   windows, groups, features, evaluation, the ranker binding
internal/graph/    union-find and link rules
internal/score/    hand-written logistic regression
internal/forest/   isolation forest — measured, and declined
internal/registry/ simulated suspect-account list, and what a graph adds to one
internal/agent/    bounded actions, the policy envelope, the fallback chain
internal/enforce/  leases, the restriction ledger, replay, the ceiling
internal/audit/    hash-chained append-only decision log
internal/latency/  three latencies and their percentiles
internal/baseline/ the per-transaction scorer WARREN has to beat
internal/aml/      AML schema, loader and patterns-file parser
internal/web/      console handlers, templates and styling
internal/dbtest/   a private schema per test run

docs/FINDINGS.md   every measurement, including the ones that went the wrong way
DECISIONS.md       every non-obvious choice, with the rejected alternative
```

## Known weaknesses, owned

- **14.3% precision at 50 alerts.** Defensible — ~22× lift over the 0.64% base
  rate of the population it scores, and production AML often runs lower — but not
  impressive. The detector is deliberately the least
  clever part of this system: given a fixed budget it was spent on making a
  wrong answer safe rather than on making the answer marginally righter, because
  in payments the second is what loses money.
- **BIPARTITE recall is 25%.** Mechanistic, not bad luck: a bipartite group has
  no intermediaries by construction, so `conservation`, `pass_through` and
  `fast_forward` — three of the model's four strongest features — are
  identically zero on it. It is the detector's real hole.
- **No GNN.** A deliberate choice, not an omission: an interpretable
  coefficient beats an embedding that cannot be defended, and the graph
  structure lives in the features. The unsupervised half of the same
  recommended architecture *was* built, and it earned a negative coefficient.
- **Synthetic primary dataset, and not Indian rails.** Ring topology is the same
  graph problem across rails; IBM AML was chosen because it ships labelled ring
  ground truth. The detector transfers; the channel filter would need
  re-deriving.

## Configuration

`DATABASE_URL` overrides the local development database. `GEMINI_API_KEY`
enables the model tiers; without it the console still runs and decides on the
deterministic fallback.
