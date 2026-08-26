# Decisions

Append-only. One entry per nontrivial choice — data structure, algorithm, or
architecture, plus the reasoning and what else was considered.

```
## YYYY-MM-DD — <short title>
**Chose:** <what>
**Why:** <reasoning>
**Alternative considered:** <what else, and why not>
**My answer before seeing yours:** <my guess, or "n/a" if none was asked>
```

Entries below dated 2026-08-22 are backfilled from `CLAUDE.md` and
`docs/FINDINGS.md` — decisions already made before this log existed. Their
"my answer before seeing yours" field is marked retrofitted rather than
fabricated. Entries from here forward should be logged live, at the point
the choice is made.

---

## 2026-08-22 — Gemini over the Claude API as the model backend
**Chose:** `gemini-3.7-flash` primary, `gemini-3.5-flash-lite` second tier, via
`google.golang.org/genai`.
**Why:** free tier covers the project's volume; the Claude API key authenticates
but the account has no API credit (the ~$89 promotional credit is subscription
credit, a separate ledger, and doesn't fund API calls) — verified with
`cmd/apitest -provider claude`. The Claude Agent SDK would be free under the
Pro plan's programmatic credit but ships only for Python/TypeScript, and
reaching it from Go would mean a sidecar running a coding-agent framework to
make one structured call — worse to defend in a panel than a direct client.
**Alternative considered:** Claude API direct; Claude Agent SDK via sidecar.
Both rejected — cost/availability and defensibility respectively.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — Logistic regression over a GNN for ranking
**Chose:** hand-written logistic regression over nine candidate-level features.
**Why:** an interpretable coefficient beats an embedding that can't be defended
in a panel interview, and the graph structure already lives in the engineered
features (conservation, density, span_hours, etc.) rather than needing to be
learned. The India BFSI report calls inductive GraphSAGE a "baseline
operational necessity" — a real gap, but the biggest paper gap for the worst
use of remaining time against a 2026-09-05 deadline.
**Alternative considered:** GraphSAGE / GNN embedding. Rejected — cost/benefit
against the deadline, and an embedding is a weaker thing to defend live than a
coefficient with a sign and a magnitude.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — Fallback account keys manufactured a signal (trap #1)
**Chose:** unidentifiable transactions become singleton accounts rather than
being pooled under a fallback key.
**Why:** falling back to the raw card tuple when `D1`/`addr1` were missing
pooled unrelated people into pseudo-accounts averaging 224 transactions, which
linked into one 236-member "ring" holding nearly all the apparent enrichment.
Refusing to guess beats inventing an account.
**Alternative considered:** keep the fallback-key pooling — rejected once the
236-member ring was traced back to it; the enrichment was an artefact, not
signal.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — Two independent shared values required for a link (trap #2)
**Chose:** account-linking now requires two independent shared values, not one.
**Why:** one shared value per link chained 2,219 individually reasonable links
into an 8,930-account "ring" (next largest was 115) — unconstrained transitivity
built a giant component instead of finding real rings.
**Alternative considered:** single shared-value linking — rejected once the
giant-component failure mode was found.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — `detect.ActivePeriod` trims the generator's quiet tail (trap #3)
**Chose:** detect and trim the volume-collapse tail of the AML dataset before
splitting into train/test.
**Why:** IBM AML stops background traffic after 2022-09-10 while laundering
already in flight plays out, leaving a stretch that is 58–73% laundering. A
70/30 temporal split landed the whole test set inside it and reported a fake
precision 0.598 / recall 1.000.
**Alternative considered:** plain 70/30 temporal split with no tail handling —
rejected once the inflated metrics were traced to the quiet tail rather than
genuine model performance.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — Hash-chained, append-only audit log
**Chose:** SHA-256 chain over content, position and predecessor for every
decision record.
**Why:** editing a row after the fact must be detectable — the audit trail is
the check on the check. `cmd/audit -tamper N` rewrites a decision in place
without touching a hash, specifically so `-verify` can be seen catching the
exact thing someone covering their tracks would do.
**Alternative considered:** plain append-only log without hashing — rejected,
gives no way to prove nothing was altered after the fact.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-22 — The policy envelope binds both ways
**Chose:** BLOCK requires ranker score ≥0.90 *and* stated confidence ≥0.80 *and*
total under the autonomous ceiling; ALLOW is withheld at ranker score ≥0.50, so
a well-scored group can't be waved through either.
**Why:** the model proposes, the policy disposes — authority can't live in the
model. Binding only the block side would still let the model silently clear
something the detector flagged; binding both sides makes the disagreement
between proposal and decision visible and is, per the CLAUDE.md, "the pitch."
**Alternative considered:** clamp only the block/deny direction — rejected,
leaves the allow path ungoverned.
**My answer before seeing yours:** n/a — retrofitted.

## 2026-08-24 — Seed the audit trail at startup with real decisions
**Chose:** on boot, if the audit log is empty, run the real assessor chain over
the top few alerts and record them; skip the ring the front page opens on, and
never touch a log that already has rows.
**Why:** an empty audit page argues against the claim it exists to prove. The
claim is "every decision leaves a record"; a blank table reads as no decisions
being recorded, not as none having been made yet. Seeding with real chain calls
rather than fixtures keeps the page honest — the first boot recorded four
decisions that all degraded to the second tier after `gemini-3.7-flash`
returned 503, and that degradation is now visible on the page without anyone
having to stage it. Skipping the hero's covering ring preserves the live
assess-in-front-of-the-audience moment on the front page.
**Alternative considered:** ship canned fixture rows — rejected, they would be
the one part of the demo that isn't real, in the component whose entire purpose
is trustworthiness. Also considered seeding in the background so boot isn't
blocked — rejected, it reintroduces the empty-page race it was meant to fix,
and 30s on an already-slow startup is not the constraint.
**My answer before seeing yours:** n/a — mechanical follow-through on an agreed
UI list.

## 2026-08-24 — Alert-budget strip on the queue, metrics page kept
**Chose:** put the precision/recall/rings/value-held tradeoff at 50, 250 and
1,000 alerts directly on the queue page, and lengthen the queue from 40 to 50
rows to match the budget being quoted. The full performance page stays.
**Why:** the queue's own numbers were unjustified on the page they appeared on —
the evidence for "precision 13.08%" lived one nav click away, which in a
five-minute pitch is a click nobody makes. The strip makes the false-positive
cost impossible to skip past, which is what the track bar asks for. Showing 40
rows while quoting a figure measured at 50 invited an obvious question with no
good answer.
**Alternative considered:** fold the whole metrics page into the queue and drop
the nav item — rejected, per-shape recall and the ranker's coefficients are the
material for the architecture-defence round and deserve room, not a strip.
**My answer before seeing yours:** n/a — mechanical follow-through on an agreed
UI list.

## 2026-08-27 — Publish three latencies, not one
**Chose:** measure and publish all three meanings of "latency" side by side —
score-per-candidate, amortised detection cost per transfer, and arrival-to-
decision — at p50/p95/p99, on stdout from `cmd/detect` and on the console's
performance page. Percentiles are nearest-rank over every retained sample, held
in a plain sorted slice.
**Why:** the BFSI report's sub-100ms bar was written for per-transaction
scoring, and WARREN's unit of decision is a group over a 72h window. Only the
first of the three clears the bar, and quoting only that one would be the
flattering framing the working style forbids. Published together they make the
actual argument: the wait exists because the evidence does, and the thing being
bought with it is a class of loss a 10ms scorer cannot see. Nearest-rank on a
sorted slice because 95k samples is under a megabyte and a t-digest or
histogram would put approximation error into the number most likely to be
challenged — bounded memory is not a constraint this project has.
**Alternative considered:** publish score latency alone and claim the bar is
met — rejected, it is true and misleading. Streaming quantile sketch — rejected,
solves a problem at a scale this does not reach, at the cost of exactness.
Interpolated percentiles — rejected, nearest-rank means every figure printed is
a measurement that actually happened.
**My answer before seeing yours:** "publish all three, p50/p95/p99, console and
stdout" — asked and answered before implementation. The aggregation structure
was left open and chosen as above.

## 2026-08-27 — Measure the timer's own floor alongside the score path
**Chose:** every run measures the cost of two clock reads with nothing between
them and prints it directly under the score-latency row.
**Why:** scoring a candidate takes ~150ns and `time.Now()` twice costs ~20ns on
this machine. Without the floor there is no way to tell a real sub-microsecond
measurement from the instrument measuring itself, and "we score in 150ns" is
exactly the claim a panel should push on. With the floor beside it the reading
is defensible: seven times the cost of taking it.
**Alternative considered:** time a batch of 95k candidates and divide —
rejected, it removes the per-call overhead but also removes the distribution,
and p95/p99 were the requirement. Ignore the issue — rejected, it is the same
"measure before believing" failure as the three artefacts already in FINDINGS.
**My answer before seeing yours:** n/a — a correctness fix inside an agreed
measurement, not a separate design choice.

## 2026-08-27 — Report steady-state and cold-start decision latency separately
**Chose:** `MeasureLatency` reports arrival-to-decision twice: over transfers
that had a full set of overlapping windows available, and over every transfer
in the run, with the excluded count stated.
**Why:** the first measurement came back at p95 66.5h against a stride bound of
24h. The cause was the harness — windows start at the ledger's first transfer,
so the first 48h of arrivals are covered by fewer windows than a running system
would have opened, and with only 10 windows that cold start is 31% of the
sample. Reporting only the all-transfers figure blames the detector for the
shape of the test file; reporting only steady state hides that the first day of
a real deployment is genuinely slower. Once separated, steady state lands
exactly on the predicted stride bound (p50 12.1h, max 24.0h), which is the
check that the number is the architecture and not an accident.
**Alternative considered:** silently drop the warm-up transfers — rejected, it
is the same class of error as trap #3 in reverse, quietly trimming data until
the number looks right. Report only the raw figure — rejected, it is a
measurement of the harness presented as a measurement of the system.
**My answer before seeing yours:** n/a — caught during verification, after the
approach was agreed.

## 2026-08-27 — Only a policy-approved block may stop money
**Chose:** in `internal/enforce`, a block leases a `frozen` restriction over the
ring's accounts and stops later transfers out of them; a `hold_for_review` leases
a `watch`, which is recorded and expires and stops nothing; `allow` leases
nothing.
**Why:** it means the gates the agent policy already applies to blocking — ranker
score ≥0.90, stated confidence ≥0.80, total under the 10M ceiling — are the only
gates on enforcement, and there is no second, looser path to freezing an account.
Making `hold` freeze money too would have created exactly that path, and would
have put the false-positive cost of the whole review queue onto people's
accounts rather than onto an analyst's time.
**Alternative considered:** hold imposes a short freeze pending review, which is
what some real systems do — rejected here because the review queue runs at 6–13%
precision, so it would be holding roughly nine innocent groups per real one, with
no human between the model and the money.
**My answer before seeing yours:** chose "restriction ledger" from three options
when asked; the tier split within it was mine.

## 2026-08-27 — Measure the interception ceiling before judging the result
**Chose:** an oracle detector — told the true ring membership, acting at the first
window closure at or after the ring's third transfer — computed per window
geometry, and the replay result reported as a share of it.
**Why:** a bad enforcement number has two causes needing opposite responses. If
stoppable money is being left on the table, build a better detector. If almost
none of a ring's money is still in flight when any windowed detector could first
see it, no detector helps and the architecture is what must change. Guessing is
how a week goes into the wrong thing. Measured: at the shipped 72h geometry the
ceiling is **10.76%** — a perfect detector could stop at most a tenth of ring
value — and it triples at 48h. The window width chosen for recall in finding 10
costs two thirds of the achievable enforcement value.
**Alternative considered:** report interception against all laundering value in
the period — rejected, it conflates the layer being weak with the layer being
given no runway, and it has no upper bound to be read against.
**My answer before seeing yours:** n/a — the ceiling was built after the first
replay returned near-zero and the cause was genuinely ambiguous.

## 2026-08-27 — Replay with a deterministic decider, not the model
**Chose:** `enforce.ThresholdDecider` proposes block/hold/allow straight off the
ranker score and hands the proposal to the real `agent.Policy`; the replay uses
it by default rather than calling Gemini.
**Why:** the published enforcement figure should not move because a provider
returned 503, and 250 alerts per geometry across four geometries is a thousand
model calls per sweep. What is being measured is what a block *does* — which
rings get blocked is the model's job, and swapping the decider changes the
former, not the latter.
**Alternative considered:** run the real chain — kept available and worth doing
once for the write-up, but not as the default, because a headline number that is
not reproducible is not a measurement.
**Risk accepted:** `ThresholdDecider` can block without a model, which
`RuleAssessor` deliberately refuses to do on a degraded path. It is named and
documented as a measurement stand-in and must never be added to the chain.
**My answer before seeing yours:** n/a — follow-through on an agreed measurement.

## 2026-08-27 — One geometry cannot serve both investigation and enforcement
**Chose:** record, but do not yet implement, that the sweep argues for two
detection passes rather than one compromise width.
**Why:** between 72h and 24h windows, detection recall at 250 alerts falls 34.7%
→ 13.5% while rings intercepted rises 7 → 30 and laundering value stopped rises
100×. A wide window sees the ring's shape; a narrow one still has something left
to freeze. Those are different objectives and the measurement says a single width
trades one against the other rather than serving both.
**Alternative considered:** pick the middle at 48h/12h, which has the best action
precision (7.88%) — rejected as a default because it is worse than 24h at the
thing enforcement exists to do and worse than 72h at the thing the queue exists
to do.
**My answer before seeing yours:** n/a — a conclusion from the sweep, not yet a
build. Flagged for a decision rather than made unilaterally.

## 2026-08-27 — Two detection passes, one role each
**Chose:** run a 72h/24h pass and a 24h/6h pass over the same ledger, each with
its own fitted ranker. The wide pass feeds the analyst queue and never leases;
the narrow pass leases into the restriction register. `Source.Enforces` makes
"detects but does not hold anyone's money" expressible.
**Why:** measured. A single width has to choose — 72h gives 39.4% detection
recall and stops 230k, 24h gives 18.4% recall and stops 12.26m. Split by role the
system takes the better number from each: 39.4% recall for the queue and 12.26m
stopped. That is a smaller claim than "two passes catch more", which is what the
first comparison appeared to show and did not.
**Alternative considered:** one compromise width at 48h/12h — rejected, it is
worse than 24h at what enforcement is for and worse than 72h at what the queue is
for. A shared ranker across both passes — rejected, the geometries raise
different candidate distributions and one fit would describe neither.
**My answer before seeing yours:** chose "escalating leases" from three options
when asked about pass authority; the split-role arrangement came out of measuring
that choice.

## 2026-08-27 — The escalating lease was built, measured, and left out of the default
**Chose:** keep `Register.Impose`'s keep-the-longer-lease merge and its
new/extended/ignored reporting, but do not let the wide pass lease in the
recommended configuration.
**Why:** the mechanism works — 2,559 escalations logged over the held-out period —
but letting the 72h pass lease buys +8 rings hit and +1.9% laundering value for
+8.4% more innocent money held, and drops action precision from 7.05% to 6.66%.
Against this project's stated posture, spending false-positive budget for a 1.9%
gain in the true positive is the wrong side of the trade. The merge rule stays
because overlapping windows inside a single pass re-lease the same accounts and
need it regardless.
**Alternative considered:** keep both passes leasing and quote the +8 rings —
rejected; it is the flattering half of a measurement whose other half is 13.6m of
someone else's money.
**My answer before seeing yours:** escalating leases, chosen from options. The
measurement contradicted the choice and the choice lost.

## 2026-08-27 — Comparisons hold total alert budget constant
**Chose:** when comparing configurations, the sum of alerts across passes is the
quantity held fixed, not the per-pass budget.
**Why:** the first dual-geometry comparison ran 375 alerts in each of two passes
against 375 in each single pass, so the dual was spending 750 against 375 and
looked better on every axis. A three-pass version looked better still, for the
same reason. Held to a constant total the ordering inverted: 24h/6h alone at 750
alerts stops 16.13m against the dual's 12.49m. Alert budget is the resource the
whole system spends; a comparison that lets one side spend triple is not a
comparison, and this is the fourth time in this project a good-looking number has
turned out to be an accounting artefact.
**Alternative considered:** report per-pass budgets and let the reader adjust —
rejected, nobody does, and the headline would have been wrong.
**My answer before seeing yours:** n/a — caught during verification.

## 2026-08-27 — Two hash chains, cross-referenced, rather than one
**Chose:** restrictions get their own append-only hash-chained table,
`account_restrictions`, with every lease and lift naming the `audit_log` entry
that authorised it.
**Why:** `audit_log` records what the system decided; this records what it did
about it. Folding both into one table meant either leaving the record type
outside the hash, which makes it forgeable, or bringing it inside and
invalidating the hash of every decision already written. Two chains keep both
properties and lose nothing, because the join is what carries the meaning: a
lease whose `decision_seq` points at a broken audit entry is money being held on
an authority that no longer verifies. Verified working — tampering with decision
4 now reports the eight accounts still frozen on its authority, by name.
**Alternative considered:** rebuild the register purely by replaying the audit
log — rejected, the audit entry carries `agent.Evidence`, which deliberately has
no account ids, and adding them means either putting account identifiers into the
model-facing evidence or extending what the hash covers. A standalone table with
no cross-reference — rejected, it loses the one property this project has spent
the most effort earning.
**My answer before seeing yours:** chose "linked + verified" from three options
when asked.

## 2026-08-27 — Enforce after recording, never before
**Chose:** `enforceDecision` runs only once the decision is committed to the
audit log, and carries that sequence number onto every lease. A lease that fails
to persist is logged and not counted as held.
**Why:** it makes "a hold whose authority cannot be named is one this system will
not place" true rather than merely intended. In the other order a log write
failure would leave money held with no record of why, which is the exact
condition the audit chain exists to make impossible.
**Alternative considered:** impose in memory first for latency — rejected, the
saving is microseconds against a decision that already took a model call.
**My answer before seeing yours:** n/a — follow-through on the agreed design.

## 2026-08-27 — Surface why nothing is frozen rather than tuning until something is
**Chose:** the holds page reads the decisions' own recorded adjustments back and
explains the zero — how many asked to block, how many were refused on the value
ceiling, how many were decided by the fallback that never blocks. Added a
`-block-ceiling` flag to `cmd/serve`, defaulting to the unchanged policy value.
**Why:** every high-scoring ring in this ledger moves 17.7m to 862m against a 10m
autonomous ceiling, so on the console's queue the freeze path is unreachable. A
page showing zero freezes reads as a broken feature when it is the envelope
working. The honest fix is to say so, not to lower the ceiling so the demo looks
better — the ceiling is denominated in currency and this dataset's amounts are
not real currency, which is a property of the data, not a reason to weaken the
policy. The flag exists so the autonomous path can be exercised at all, and it
logs loudly that it is a demonstration setting.
**Alternative considered:** quietly lower `BlockMaxAmount` to fit the dataset —
rejected, it is tuning a safety limit for appearances, and it would be the one
number in the demo that was chosen to make the demo work.
**My answer before seeing yours:** n/a — found while verifying that the freeze
path was reachable at all.
