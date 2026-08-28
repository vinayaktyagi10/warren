# WARREN — demo runbook

Internal. Not a README, not for publication. This is the sheet to hold while
presenting: what to run, what should appear, what to say, and what to do when
something does not come back.

Measured against the build of **2026-08-29** on the IBM AML HI-Small ledger.
Every number below was observed on this machine, not copied from a document.

---

## 0. Canonical demo facts

Everything below is measured on this build. If a document disagrees with this
table, this table is the one that was run.

| | |
|---|---|
| Hero ring | **#270**, a **FAN-IN**, 9 labelled transfers, none caught by the per-transaction baseline |
| Console position | **analyst rank 7** (72h/24h pass); seeding skips it so it can be assessed live |
| Candidate covering it | 19 accounts · 31 transfers · **7.5M moved** · conservation **0.527** · pass-through 0.526 · span 62.5h |
| Ranker score | **0.979** |
| Ceiling override example | **rank 1** — 44 accounts, 696M, score 0.987 (side-trip only) |
| Queue headline | 21,483 candidates over 599,689 transfers; at 50 alerts **14.32% precision / 22.62% recall / 39 of 182 rings / 3.13bn held** |
| Lift | **~22×** over the **0.64%** base rate of the ACH-filtered population scored (not the ~140× the raw ledger's 0.1% would give) |
| Enforcement queue | 24h/6h pass; top alerts 60.7k–5.8M; rank 7 there is 59 accounts and exceeds the 25-account limit |
| Audit | two independent SHA-256 chains; `verify` before, tamper, `verify` after names the **first** break only |
| Fallback | `gemini-3.7-flash` → `gemini-3.5-flash-lite` → deterministic rule, which holds and **never blocks** |

## 0b. The one-paragraph version

Start the console **before** the pitch begins, walk the six pages in order, run
one live assessment, tamper with the audit trail, and finish on `-offline`.
The only step the system cannot promise is an autonomous freeze — that needs the
model to ask for one, and the deterministic tier never will. Everything else is
reproducible.

---

## 1. Prerequisites

| Thing | Check | Fix |
|---|---|---|
| Go 1.27 | `go version` | — |
| Postgres 17 | `pg_isready -h localhost` → `accepting connections` | `sudo systemctl start docker && sudo docker compose up -d` |
| Loaded ledger | `psql "$DB" -tAc "select count(*) from aml_transactions"` → 5,078,345 | `go run ./cmd/aml-ingest -data data/aml -set HI-Small` (~55s) |
| Gemini key | `go run ./cmd/apitest -provider gemini` | put `GEMINI_API_KEY=…` in `.env` (gitignored) |

`DB` here is `postgres://warren:warren@localhost:5432/warren` — the compose
file's local development credentials, not a secret.

This account is **not in the `docker` group**, so every `docker` command needs
`sudo`. `sudo systemctl start docker` alone is not enough; the second half needs
it too.

**The daemon does not survive a reboot on this machine.** Start it first, always.

---

## 2. Startup — two modes, and they demonstrate different things

**Pick the mode before you start, and say which one the room is looking at.**

| | **Online** (`go run ./cmd/serve`) | **Offline** (`go run ./cmd/serve -offline`) |
|---|---|---|
| Start-up | **~130–145s** — 15s pipeline, then ~2 min seeding eight decisions through a live model | **~16–19s** — pipeline only, no model calls |
| Assessor | `gemini-3.7-flash` → `gemini-3.5-flash-lite` → deterministic rule | deterministic rule only |
| Needs | `GEMINI_API_KEY` in `.env`, network, remaining daily quota | Postgres only |
| Shows | a real model reading the evidence; the policy overriding a real proposal; live fallback between tiers | that the system still decides with no model at all, and what a bounded decision looks like on the degraded path |
| Autonomous freeze | **possible, not guaranteed** — needs the model to propose `block` | **never** — the deterministic rule does not block, by design |

**Offline mode is not an autonomous-freeze demonstration and must not be
presented as one.** It is the *opposite* demonstration: the degraded path holds,
records, and freezes nothing. That is the point being made there.

The primary model's free tier allows **~20 `gemini-3.7-flash` calls per day**
(see §4). Once that is spent, online mode still works — `gemini-3.5-flash-lite`
answers everything — but an autonomous block becomes less likely and cannot be
promised.

Measured cold-start times:

| Command | Time | Notes |
|---|---|---|
| `serve -offline` | **16–19s** to `console on http://localhost:8080` | pipeline only |
| `serve` (online) | **130–145s** | 15s pipeline, then ~2min seeding eight decisions through a live model |
| `cmd/detect` | **7.1s** | full detection, fit, held-out evaluation, latency |
| `cmd/audit -verify` | **0.14s** | |
| `cmd/assess -top 5` | **14–90s** | five live model calls; a 429 fails fast, a 503 retries slowly |

The online path is slow because seeding makes eight real model calls and a 503
can take 20–30s to give up. **Start the server before you start talking.** Do
not restart it in front of the room.

The startup log is worth a glance before presenting — it names the hero case
(`hero case: ring 270, 9 transfers`) and whether seeding ran or found an
existing log.

---

## 3. Demo sequence

### 1 — The blind spot (`/`)

**Action:** open `http://localhost:8080/`.

**Should appear:** confirmed laundering ring **#270**, a FAN-IN of 9 transfers,
drawn as a graph. Beside it, where a per-transaction scorer put those same nine
transfers: **0 of 9** caught while alerting on the riskiest 10% of all traffic,
best single transfer ranked top 11.9%, and **21,071 alerts** needed to see even
one of them.

**Point out:** every one of these transfers looks ordinary on its own, and that
is the design of the ring, not a weakness of the baseline. The baseline has
account velocity and counterparty counts — the features legacy systems actually
use. It is not a straw man; at an equal budget of 1,460 flagged transfers it
recovers 21 complete rings to WARREN's 34. *The gap is structural: the evidence
of a ring is not inside any single row.* And **68 of 208** held-out rings have
not one transfer in the riskiest 10%.

### 2 — Alert queue (`/queue`)

**Action:** click *Alert queue*.

**Should appear:** 21,483 ranked candidates over a 599,689-transfer ledger, 50
rows drawn, and the budget strip: **14.32% precision / 22.62% recall / 39 of 182
rings / 3.13bn legitimate value held at 50 alerts**, then 250 and 1,000.

**Point out:** the value-held column. Working further down the queue finds more
rings *and* holds more innocent money, and there is no budget where both
improve. That column is why the queue is drawn at 50 rather than at the number
that flatters recall.

### 3 — Ring detail (`/ring/7`)

**Action:** from the blind-spot page press **Assess this ring**, or open
`/ring/7` directly. Rank 7 is the candidate covering hero ring #270, and seeding
deliberately skips it so there is something left to decide live.

**Should appear:** 19 accounts, 31 transfers, **7.5M moved**, conservation
0.527, pass-through 0.526, ranker score **0.979**; the money drawn as a graph
with sources, intermediaries and sinks separated; the policy envelope stated on
the page; and *9 of 31 transfers labelled laundering* under a heading saying the
truth was withheld from the assessor.

**Point out:** conservation — how closely value entering an intermediary leaves
it again. Near 1 is mule behaviour. It is the strongest single separator in this
data at 15× between rings and ordinary clusters, and it is a *number*, not an
embedding: it can be argued with.

Also point out what is **not** in the evidence bundle: no name, no reference, no
memo. A party under investigation has no channel to address the model judging
them.

### 4 — The live assessment

**Action:** press **Assess this ring**. Expect **2–25 seconds** — say so before
pressing, and keep talking while it runs.

**Should appear:** a decision with the answering tier named, the model's
rationale, and — when the policy intervened — a *policy overrode* pill and the
reason. Recorded as an audit entry with its hash.

**Point out:** *the model proposes, the policy disposes.* Model output is
untrusted input: schema-constrained to three actions, then clamped in code.
The envelope binds **both** ways — it will not block on weak evidence, and it
will not let the model wave through a group the ranker scored highly.

**What rank 7 actually returns is model-dependent.** At 7.5M it is under the 10M
ceiling and scores 0.979, so a proposed `block` here is *approved*, not
overridden — and still freezes nothing, because the 72h/24h analyst pass never
leases. The response says so: *"no hold placed: the 72h/24h pass feeds the
analyst queue and does not lease."* That is the point to make either way.

**Stay on rank 7.** It is the main example precisely *because* it is not
dominated by the value ceiling: at 7.5M the envelope has to decide on the
evidence rather than reflexively refusing on size, so the whole argument —
detection, explanation, proposal, policy, record — is visible in one screen.
Rank 1 scores higher and is not a better demonstration.

If the room specifically asks to see the ceiling refuse a block, take a
30-second detour to **`/ring/1`** (44 accounts, **696M**, score 0.987), where any
proposed block is refused on value — *above the ceiling a person decides,
however certain the machine is* — and then come back. Entry 3 on the audit page
(862M, proposed block, clamped) already shows the same thing from the seeded
log, without spending a model call.

### 5 — Audit trail (`/audit`)

**Should appear:** every decision, newest first, each with its action, the
proposal it came from, the answering tier, the rationale, any degradation notes,
and its hash.

**Action:** press **Verify chain** → `valid: true`, N entries.
Then press **tamper** on a decision that froze accounts (with default seeding,
entry **8**, ring 100004). Then **Verify chain** again.

**Should appear:** `CHAIN BROKEN at entry 8 — content hashes to a24a3dcf86d0…
but the row carries d9985055c000… — this entry was edited after it was written`.

**Point out:** the tamper button edits a decision in place and *does not touch a
hash* — exactly what someone covering their tracks would do. The hash covers the
content, the position, and the predecessor, so the break is detectable without
holding a copy of the original. And only the *first* break is reported: every
entry after it inherits the break, so listing them all would be noise.

Same thing from the terminal, which is stronger if the room is technical:

```bash
go run ./cmd/audit -verify     # exits 1, and names the consequence
```

It lists the accounts **still frozen on the broken entry's authority** and says
*"these restrictions no longer have a verifiable authority behind them."*

### 6 — Holds in force (`/holds`)

**Should appear:** what is frozen, what is watched, for how long, on whose
authority, with a **lift** button; and below it the enforcement queue — the top
12 alerts of the 24h/6h pass with everything the block gate reads on each row.
After the tamper in step 5, a red banner: *"N holds rest on an authority that no
longer verifies."*

**Point out:** two geometries, one role each. The 72h/24h pass feeds the analyst
queue and never leases whatever it decides; the 24h/6h pass is the only one with
authority to hold. That is measured, not stylistic — letting the wide pass lease
too cost **+8.4% more innocent money held for +1.9% of the laundering value**.

Then the number that matters more than the interception: an oracle detector told
the true ring membership, acting the instant any windowed detector could see the
ring, could stop only **10.76%** of ring value at 72h windows and **31.75%** at
24h. *Nine tenths of a ring's money has already moved before anyone can see the
ring.* Window width is decision latency is the enforcement ceiling. WARREN
reaches 0.66% of that ceiling, which is small, and is stated as small.

**Optional, live:** press **Assess** on enforcement rank **7** (59 accounts,
5.8M, score 0.963). If the model proposes block, the policy approves it and the
enforcement layer then declines the entire lease: *"declined to freeze: ring
spans 59 accounts, above the 25-account limit on a single automated action; a
person decides."* Nothing is partially frozen. All-or-nothing is the whole
point — a system that freezes the first 25 of 59 accounts has invented a subset
nobody authorised.

### 7 — Degraded (`-offline`)

**Action:** stop the server and restart with `-offline`, or have a second
instance already running on `-addr :8081`. **Prefer the second instance** — a
cold start is 16 seconds of silence.

**Action:** assess anything, on either route.

**Should appear:** a decision, from `deterministic-rule`, with **`held: 0`** on
the analyst route and a **watch, never a freeze**, on the enforcement route.

**Point out:** the chain is `gemini-3.7-flash → gemini-3.5-flash-lite →
deterministic rule`, and `Chain.Assess` **cannot return an error**. A risk system
that stops deciding when its provider is unreachable has moved the outage onto
the merchant. So the question is never whether to have a fallback but what it
does — and this one **will hold but will not block**, because an autonomous
block on a degraded path is exactly the decision that should wait for a person.

---

## 4. Fallback — when something does not come back

### The primary model is unavailable

Nothing to do. It falls through and the answering tier is named on screen; that
*is* the demonstration. Observed during validation: 503 UNAVAILABLE, 504
DEADLINE_EXCEEDED, a client-side deadline, and 429 RESOURCE_EXHAUSTED. Every one
still returned a bounded decision.

**Budget the primary model.** The free tier allows **20 `gemini-3.7-flash`
requests per project per day**. Startup seeding spends 8. Rehearsing a full walk
spends the rest. If it is exhausted, `gemini-3.5-flash-lite` answers everything
and the demo still works — but you cannot show the primary answering. *Do not
rehearse the live-assessment step on demo day.*

A 503 can take 20–30s to give up before falling through; a 429 fails in about a
second. Say "this is waiting on the model" rather than standing in silence.

### It takes longer than 30 seconds

`Chain.Timeout` is 30s per tier, and the HTTP write timeout is 3 minutes, so the
worst case is roughly a minute before the deterministic rule answers. It will
answer.

### Postgres needs restarting

```bash
sudo systemctl start docker && sudo docker compose up -d
pg_isready -h localhost
```

The `pgdata` volume is persistent — the loaded ledger and both chains survive a
container restart. Re-ingesting is never necessary for this.

### The server needs restarting

Startup is 16s offline, ~2min online. **Both chains persist**, so a restart does
not lose the trail — and seeding will *skip*, logging `seed: audit log already
holds N decisions, leaving it alone`. That is correct behaviour, not a failure.

### A candidate has already been assessed

Assessing again is safe and appends a **new** entry; nothing is overwritten. The
ring page shows the most recent decision. If you want a clean live assessment,
either use a rank seeding skipped (rank 7 by default) or reset (§5).

### A hold already exists

Holds lapse on their own after 24h and show as lapsed. To clear one now, press
**lift** on its row: that appends a lift event and never rewrites the original
impose row, which is the property to point at. Lifting an account that is not
held is accepted and records a no-op lift event — harmless, but it does add a
line to the ledger, so do not do it idly.

---

## 5. Reset to the expected demo state

Both chains, or the holds outlive the decisions that authorised them and
everything shows as orphaned:

```bash
docker compose exec -T postgres psql -U warren -d warren \
  -c "TRUNCATE audit_log RESTART IDENTITY;" \
  -c "TRUNCATE account_restrictions RESTART IDENTITY;"
```

(with `sudo docker` on this machine), then **restart the server** so seeding
runs again on the empty log.

This touches only the two append-only chains. It does not touch the ledger,
the labels or anything ingested. There is no other supported reset — do not
edit rows by hand, which is what the tamper button exists to simulate.

Truncating one and not the other is the mistake to avoid: holds whose
authorising decision is gone will render as orphaned on `/holds` and the demo
will look broken before it starts.

---

## 6. Key talking points

- **Graph recall.** The graph pass finds 270 of 363 rings but raises 95,318
  candidates at ~1% precision. Detection and ranking are deliberately separate
  jobs: the pass is built for recall, and ranking is what makes the output
  something a team could work.
- **Temporal ranking.** Twelve standardised features, logistic, hand-written.
  `pass_through` +0.675, `fast_forward` +0.557, `burstiness` +0.534,
  `conservation` +0.508. A coefficient states what the model believes; an
  embedding does not. The temporal features took STACK from 42.1% to 68.4% at
  1,000 alerts. `max_hour_share`, `density` and `span_hours` carry nothing and
  are kept so the fit says so itself.
- **Policy as authority.** Bounds enforced in code, not requested in a prompt.
  Block needs score ≥0.90 **and** confidence ≥0.80 **and** total ≤10M. Allow is
  withheld at score ≥0.50. An unrecognised action is a malformed response and
  lands on review. The original proposal is preserved beside the final decision
  so the disagreement is visible.
- **Audit hash chain.** SHA-256 over content, position and predecessor, in two
  separate chains. Every restriction names the decision that authorised it, and
  enforcement runs only after the decision is committed — so a hold whose
  authority cannot be named is never placed.
- **Model fallback.** Three tiers ending in one that cannot fail and never
  blocks.
- **Known limitations, stated first rather than defended:**
  - **BIPARTITE 25%** at 1,000 alerts. Say it in three parts, and keep them
    apart — a panel will push on exactly this. **Measured:** a labelled
    BIPARTITE ring is a set of *disconnected* one-transfer sender→receiver
    pairs, so each pair fails the three-account candidate requirement and the
    ring never becomes a candidate to rank. **Measured:** four corroborated
    linking rules and two partition features were built and tested; none
    recovered a single additional ring, all cost overall recall, and all
    inflated the largest component. **Interpretation:** this data does not
    appear to carry an independent signal that would safely bind those pairs.
    Do *not* say the simulator omitted one deliberately, and do *not* say a
    working representation is impossible — neither is established. The claim is
    narrow: on this data, with these hypotheses, nothing recovered BIPARTITE
    without damaging everything else. FINDINGS §19–§21.7.
  - **STACK 68%**, because 17 of 32 rings run longer than the 72h window.
  - **14.3% precision at 50 alerts** — **~22× lift** over the 0.64% base rate of
    the ACH-filtered population it scores. Quote that, not the ~140× the raw
    ledger's 0.1% would give: the channel filter drops 88% of the ledger while
    keeping 86.6% of the laundering, so most of the larger multiple is the
    filter's work rather than the ranker's. The line: *the detector is
    deliberately the least clever part; given a fixed budget I spent it on
    making a wrong answer
    safe rather than on making the answer marginally righter.*
  - **Synthetic primary dataset, and not Indian/UPI.** Chosen because it ships
    labelled ring ground truth, without which none of this could be honestly
    measured.
  - **No GNN**, deliberately. The unsupervised half of that recommended
    architecture *was* built and measured pointing the wrong way — the isolation
    forest fits at −0.288 — which is a better answer than not having tried.
  - Top-ranked candidates nearly all classify **MIXED**: accurate, visually dull.

---

## 7. What the demo cannot promise

**An autonomous freeze is model-dependent.** A block requires a model to propose
one, and the deterministic tier never does — so `-offline` can never freeze, by
design. During validation, seeding with the primary model available froze 3
accounts; the same four alerts with the primary rate-limited and
`gemini-3.5-flash-lite` answering came back `hold_for_review` and froze nothing.
The policy behaved identically both times; only the proposal differed.

So do not open by promising the room a freeze. If one has happened, show it. If
it has not, the holds page is still the right screen: watches are leases too,
the enforcement queue shows what the gate reads on every row, and `cmd/audit
-verify` after a tamper still names the consequence.

**Do not lower `-block-ceiling` to make this look better.** It exists as a
demonstration setting, it logs loudly that it is one, and the freeze path is
already reachable at the real default on the enforcement pass.
