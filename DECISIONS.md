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

## 2026-08-27 — Test the two hash chains against Postgres, not a fake
**Chose:** the audit log and the restriction ledger are tested against a real
database, in a throwaway schema per test (`internal/dbtest`), rather than behind
an in-memory interface.
**Why:** the property both chains claim — that two concurrent writers cannot
fork the log — lives entirely in what Postgres does with a lock inside a
transaction. A fake reimplements that behaviour and then passes, which tests the
fake. Taking a private schema and setting `search_path` on the pool gives each
test its own copy of every table, so the suite cannot touch loaded data or a
demo's audit log, and it skips rather than fails where no database is reachable.
It found two bugs in the first run.
**Alternative considered:** a repository interface with a memory implementation —
rejected, it would have hidden both bugs. A shared test database with truncation
between tests — rejected, tests then cannot run in parallel and a crashed run
leaves the developer's own data truncated.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Serialise chain appends with an advisory lock
**Chose:** `pg_advisory_xact_lock` on a fixed per-chain key at the top of every
append transaction, replacing `SELECT ... ORDER BY seq DESC LIMIT 1 FOR UPDATE`.
**Why:** the row lock did not do what its comment claimed. A second writer
blocked on it re-reads the row it was waiting on, not the newer row the first
writer inserted meanwhile, so both chain onto the same predecessor; and on an
empty table it locks nothing at all, which is the state at the first two
decisions of every run. The log forks and then fails to verify, reporting
tampering on a log nobody touched. That is the worst failure this component can
have — it destroys trust in the single signal the component exists to give. An
advisory lock is held for the length of the transaction, needs no row to exist,
and costs one round trip. Found by a test that ran eight concurrent writers.
**Alternative considered:** `LOCK TABLE ... IN EXCLUSIVE MODE` — works, but
blocks readers' concurrent maintenance for no gain. SERIALIZABLE plus a retry
loop — correct, but it puts retry logic in the one code path that must be
simple enough to read and believe.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Hash timestamps at the precision the storage keeps
**Chose:** the restriction chain digests instants truncated to microseconds in a
fixed-width layout, matching what the decision log already did.
**Why:** it was RFC3339Nano, and the consequence is that the restriction chain
had never verified once — not in any demo, not in any run. Postgres keeps
`timestamptz` to the microsecond, so the nanosecond digits in the digest are
gone by the time the row is read back and every entry recomputes to something
else. RFC3339Nano also drops trailing zeroes, so two instants at matching
precision can still render differently. A verifier that reports tampering on
every untouched row is worse than no verifier, because the one real break is
then indistinguishable from the noise it always emits. Found the first time a
test read a written ledger back.
**Alternative considered:** store the digest input alongside the row — rejected,
it doubles the storage and makes the hash cover a copy of the data rather than
the data. Round-trip the timestamp through the database before hashing —
rejected, it makes the digest depend on a second query succeeding.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Temporal features, measured against their own absence
**Chose:** three scale-free temporal features — finite-size corrected burstiness,
densest-hour share, and forwarding speed — behind a `FeatureSet` selector, so
`-features base` and `-features temporal` run the same pipeline and differ only
in what the ranker sees.
**Why:** the only thing the ranker knew about time was `span_hours`, which the
fit has always said carries nothing, and correctly — a span describes the two
ends of a group and nothing about the arrangement between them. The selector is
the point as much as the features: a feature added without a way to run the
pipeline without it is a feature nobody can ever measure. Result: precision up
at every budget under 2,500, and STACK recall at 1,000 alerts from 42.1% to
68.4%. BIPARTITE did not move at all, which is mechanistic — a bipartite group
has no intermediaries by construction, so `conservation`, `pass_through` and
`fast_forward` are all identically zero on it.
**Alternative considered:** raw Goh–Barabási burstiness — rejected after
checking its ceiling, `(sqrt(m)-1)/(sqrt(m)+1)`, which varies with group size;
it would have been a proxy for a quantity the model already has two features
for. Hour-of-day and night-share features — not built, because the generator has
no diurnal structure to find and the feature would have measured the simulator.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — A ranker carries the feature set it was fitted on
**Chose:** `detect.Ranker` binds the fitted model to its `FeatureSet`, and
`score.RingFeatureNames` is deleted.
**Why:** the names were a second source of truth, and adding three features made
it stale immediately — silently. Nothing failed: the console simply stopped
printing the coefficients past the end of the old list, and the ones it did
print would have been correct only by luck. Worse, `Predict` on a vector from
the wrong set is not a compile error, it is a wrong number. Binding the two
makes the mismatch unrepresentable rather than merely unlikely, and `ScoreAll`
removed the same three-line vector-then-predict loop from five call sites.
**Alternative considered:** keep the list in `score` and add a test that the two
agree — rejected, it detects the drift instead of preventing it, and only for
the one pairing the test happens to name.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Report recall per ring shape at the alert budget too
**Chose:** `EvaluateRankedByShape` breaks recall out per shape at each budget,
sorted worst first.
**Why:** the unranked report has always shown per-shape recall, on the stated
principle that one average must not hide a shape the detector never finds. The
ranked report — the one describing what an analyst actually receives — showed
only the average, so the principle was being applied to the number nobody acts
on and not to the number everybody quotes. It is also what made the STACK result
visible; at the aggregate the temporal features looked like a rounding change.
**Alternative considered:** report shapes only at one chosen budget — rejected,
choosing the budget after seeing the numbers is how a flattering one gets
picked. Every budget carries the breakdown; `-shape-budget` only selects which
is printed.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Simulate the suspect registry, and measure the graph against it
**Chose:** `internal/registry` generates an I4C-style mule list from the labels
with partial coverage, an exponential reporting delay and a false-report rate,
exposes it as one candidate feature, and ships a control that ranks by the list
alone.
**Why:** the interesting claim is not "a registry helps" — it obviously does —
but that a graph turns each reported account into several implicated ones, which
is this project's whole thesis in the language of a stated national priority. So
the measurement is amplification: laundering accounts surfaced *excluding the
ones the list already named*, because counting those is counting the input as
output. The control is the part that makes it falsifiable: ranking by the list
alone finds 5 rings in 50 alerts at realistic coverage against the fused model's
34.
**Alternative considered:** ingest a real registry — none exists publicly
alongside a labelled ledger. Seeding the registry from the detector's own past
alerts — rejected, it would make the feature a memory of the detector's own
output and every number circular. Reporting only the fused result — rejected,
without the list-only control the number is unfalsifiable.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — The registry stays off by default
**Chose:** `-registry` is opt-in; the shipped configuration is the temporal set.
**Why:** the list is simulated from the labels, and its precision at 50 alerts —
40.7% at 30% coverage against 14.3% without — is by far the best number in this
project. Quoting it as the headline would be quoting a number produced by
telling the detector part of the answer. It is a measured capability with its
sensitivity curve published, which is a different claim from a result.
**Alternative considered:** default it on and caveat the number — rejected, the
caveat never travels as far as the number.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Fix the reproducibility hole the registry exposed
**Chose:** components are iterated in sorted order rather than straight off a
map, and every score ordering goes through one `detect.RankOrder` that breaks
ties by position.
**Why:** two runs of the same command on the same ledger gave different answers —
21 rings at 50 alerts on one run, 24 on the next. `detectWindow` ranged over a
map of components, so candidate order followed Go's map seed, and with it the
fitting set, the float summation order inside the fit, and the resolution of
every score tie. It had gone unnoticed because the published feature sets happen
to produce few ties; the registry feature is zero for nearly every candidate and
produces long blocks of them, which is what surfaced it. A project whose entire
claim is that its numbers were measured cannot have the same pipeline reporting
two answers.
**Alternative considered:** seed the map iteration — not a thing Go offers.
Accept it as noise and quote a range — rejected, it is not noise, it is
non-determinism, and the fix is fifteen lines.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Fuse the isolation forest as a feature, not as a second opinion
**Chose:** the forest's score is handed to the logistic model as one more
feature, rather than averaged with the ranker's score or thresholded into a
second alert stream.
**Why:** it makes the question falsifiable. Averaging two scores requires
choosing a weight, and any weight chosen by hand is a claim nobody measured; a
threshold requires choosing a cut-off with the same problem. As a feature the
fit chooses the weight and the coefficient is printed, so "was the unsupervised
half worth anything" has a published number. The number came out **negative**
(−0.288): being anomalous lowers suspicion, because a laundering ring is
engineered to look ordinary and the genuinely unusual clusters here are unusual
and innocent. A hand-chosen positive weight would have made the system worse and
nothing would have said so.
**Alternative considered:** score fusion by rank averaging — rejected for the
above. A separate anomaly queue — rejected, it doubles the alert budget, and
§"comparisons hold total alert budget constant" already settled what that does
to a comparison.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Isolation forest measured and not adopted as the default
**Chose:** `-features anomaly` exists; the shipped default stays `temporal`.
**Why:** it buys a 31% cut in false-positive value held at 1,000 alerts for
essentially unchanged ring recall — a real argument for a project that says it
spent its budget on making a wrong answer safe. It costs 16 points of STACK
recall and 6 of BIPARTITE, which are the two shapes this project has been most
open about being bad at. Buying a cost reduction with recall on the detector's
weakest structures is the wrong direction, and the alternative reading is close
enough that the flag exists and both tables are published rather than one.
**Alternative considered:** adopt it and lead with the false-positive number —
rejected; it would mean quietly regressing the two weakest shapes while
advertising an improvement.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — The console runs both geometries; only the narrow one leases
**Chose:** the console runs a 72h/24h analyst pass and a 24h/6h enforcement pass
with its own fitted ranker, and `enforceDecision` takes which pass a ring came
from as an argument. The wide pass records decisions and places no holds.
**Why:** §14 measured that letting the wide pass lease costs +8.4% more innocent
money held for +1.9% of the laundering value, and the console was doing exactly
that. A documented finding contradicted by the demo is worse than no finding,
because the demo is what people look at. A separate ranker rather than a shared
one, because a 24-hour window produces a different candidate population than a
72-hour one and a model fitted on one puts the other's scores — the number the
block gate reads — on a scale nothing measured.
**Alternative considered:** merge both passes into one queue labelled by
geometry — rejected, the quoted precision at 50 alerts describes the wide pass,
and a queue showing something else invites the obvious question. A boolean on
the existing assess endpoint — rejected, the two differ in whether they can take
someone's money, and that belongs in the route rather than in a parameter one
typo away from being wrong.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-27 — Namespace ring ids across the two passes
**Chose:** enforcement-pass rings are recorded at `100000 + rank`.
**Why:** both passes rank from 1, so "ring 3" in the audit log would name two
different groups of accounts depending on which pass wrote it — and the holds
page joins on exactly that number. An ambiguous identifier inside a record whose
whole purpose is to be unambiguous later is not cosmetic.
**Alternative considered:** a pass column on `audit_log` — better normalised,
but it changes what the hash covers and invalidates every entry already written,
which §"two hash chains" already refused once for the same reason.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-28 — Diagnose the bipartite hole before building for it
**Chose:** probe how labelled BIPARTITE rings actually reach the candidate set
before writing a single feature, and let the probe decide what gets built.
**Why:** the recorded diagnosis — that `conservation`, `pass_through` and
`fast_forward` are zero on bipartite groups and therefore cannot reach the shape
— turned out to be two claims welded together, one true and one false. All three
features really are exactly zero on all 5,297 candidates `classify` names
BIPARTITE, without one exception. But **not one held-out candidate in that
population carries a labelled ring**, and the candidates that do cover a
BIPARTITE ring are MIXED, with *higher* conservation and pass-through than other
ring-bearing candidates. Building a feature on the recorded diagnosis would have
been building for a population that contributes nothing to the number being
fixed. The actual cause is that a labelled BIPARTITE ring is N disjoint
sender→receiver pairs — median 4 transfers in 4 components, largest component 1
— so a detector that groups by connectivity has nothing to group.
**Alternative considered:** implement the obvious feature list (degree symmetry,
partition entropy, edge concentration) straight from the brief and measure after
— rejected; it would have produced the same rejection three days later with no
explanation attached, and the explanation is the deliverable.
**My answer before seeing yours:** the structural limitation is the primary
deliverable; verify the zero-features claim, verify the experimental population
is tiny, run the ranking experiment once as a sanity check without tuning, and
treat any result as exploratory rather than as grounds to change the ranker.

## 2026-08-28 — Three bipartite features, each in its own feature set
**Chose:** `partition_balance`, `pair_reuse` and `amount_uniformity`, exposed as
`bip-balance`, `bip-reuse`, `bip-uniform` and `bipartite` — four sets, each
extending `temporal` in place, rather than one set carrying all three.
**Why:** a bundled set can only ever answer "did these three together help",
and the answer here would have been "yes" for a reason that has nothing to do
with bipartite structure. Split into arms, the ablation says that the two
features actually built for the two partitions did nothing and the bystander did
everything, which is a different and much more useful finding. This is the same
reason §5 gives for every feature having a set: a feature nobody can run the
pipeline without is a feature nobody can ever measure.
**Alternative considered:** add all three to the existing `all` set — rejected,
`all` carries a published number (113 rings at 1,000 alerts, §17) and widening
it would silently restate that number as a measurement of something else. A
generic "any subset of features" flag — rejected, it makes every published set
name unstable and there are four arms, not forty.
**My answer before seeing yours:** n/a — the feature list was left to me once
the diagnosis was settled.

## 2026-08-28 — Reject both bipartite features on the measurement
**Chose:** `partition_balance` and `amount_uniformity` are rejected and the
shipped default stays `temporal`.
**Why:** `partition_balance` fits at −0.090 and leaves BIPARTITE recall at
exactly 25.0%, costs a ring overall and four points of FAN-OUT. `amount_uniformity`
fits at −0.220 — negative, which already contradicts the hypothesis that
motivated it — leaves BIPARTITE at 25.0%, costs two rings and five points of
STACK. The feature aimed most directly at the two partitions did nothing for the
shape defined by them, and that null result is the evidence for the structural
conclusion. Keeping either because some other number improved would be keeping a
feature for a reason unrelated to why it was built.
**Alternative considered:** keep `partition_balance` on the +0.9pt precision at
50 alerts — rejected, precision at one budget against a ring lost at 1,000 and a
regressed typology is choosing the flattering budget after seeing the numbers,
which §"comparisons hold total alert budget constant" already refused once.
**My answer before seeing yours:** treat any result as exploratory, not as
evidence for changing the production ranker.

## 2026-08-28 — `pair_reuse` is reported, not adopted
**Chose:** `pair_reuse` stays behind `-features bip-reuse`; `temporal` remains
the default despite `pair_reuse` producing the largest single-feature effect
ever measured here — coefficient −0.922, precision at 50 alerts 14.32% → 24.10%,
seven more rings at 1,000, 6.9bn less innocent value held, no typology regressed.
**Why:** it is not the feature under test. It measures edge concentration, not
bipartite structure, and it moved BIPARTITE by one ring out of sixteen — inside
the noise, since seven of those sixteen held-out rings are a single transfer.
Its mechanism ("a ring spends each relationship once") may be a property of the
simulator rather than of laundering, and nothing measured here separates the
two. It absorbs `density` (−0.432 → −0.117, r = +0.732) and `log_accounts`
(+0.367 → +0.066), so part of the gain is re-attribution. And it was produced by
a single exploratory run that was set up as a sanity check, which is the exact
circumstance under which this project has been wrong five times.
**Alternative considered:** adopt it as the default and lead with 24.10%
precision at 50 alerts — rejected; it is the best number in the project and it
arrived by accident, on synthetic data, from a run nobody designed to test it.
Delete it as off-topic — rejected, a measured effect that large is worth leaving
runnable with its caveats attached, the same way `-features anomaly` keeps a
negative result runnable.
**My answer before seeing yours:** run it once as a sanity check, without tuning
around it, and treat the result as exploratory.

## 2026-08-28 — Test the linking hypotheses in a harness, not in the detector
**Chose:** a separate measurement harness whose baseline arm reproduces
`detectWindow` exactly, rather than putting the candidate rules behind a flag in
the production detector.
**Why:** the question is whether any second-order link is *safe*, and the honest
way to ask it is one where a wrong answer cannot reach the shipped path. A flag
on `detect.Config` would have meant every hypothesis lived in the file that
carries the published numbers, one default away from changing them. The
harness's baseline reproducing 95,318 candidates / 270 rings / 0.18% purity /
BIP 17/45 / STACK 13/40 is what makes its other arms believable; without that
check the arms would be measuring a reimplementation.
**Alternative considered:** add the rules to `internal/detect` behind a config
field — rejected for the above, and because the answer turned out to be C, which
would have left four dead rules permanently in the production file. Measure on a
sample of windows for speed — rejected, the giant-component behaviour is exactly
what a sample would miss.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-28 — Require a matched control for every structural claim
**Chose:** every property of the BIPARTITE rings is reported beside the same
property measured on random transfers drawn from the same window and matched in
group size.
**Why:** "the amounts inside a ring are similar" and "the transfers are close in
time" are both true and both worthless, because they are true of any N transfers
drawn from a 72h window. The control is what turned the timing observation from
a lead into a rejection (34.3h span against the control's 46.7h — a difference,
but nowhere near enough to link on) and what killed the shared-counterparty
hypothesis outright, since the control shares counterparties *more often* than
the rings do (44.8% against 24.1%). Four of the six hypotheses in the brief die
on the control alone.
**Alternative considered:** compare BIPARTITE rings against other typologies
only — rejected; the other typologies are connected, so every difference would
be explained by connectivity and nothing would be learned about linkability.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-28 — (C) Candidate construction is not altered
**Chose:** no change to linking, to candidate construction, or to the detector.
BIPARTITE stays at 17/45 at the graph pass and 25.0% at 1,000 alerts.
**Why:** four corroborated hypotheses were measured end to end and not one
improved BIPARTITE recall — the best two left it at exactly 17/45 and the other
two lowered it. All four cost overall graph recall (270 rings down to as low as
190) and damaged every other typology. All four inflated the largest component,
from 3,220 accounts to between 5,558 and 48,172; the uncorroborated timing form
reaches 101,334 in a single window, which is trap 2 at 11× scale. The mechanism
is that the ring's own pairs sit 12.5h apart at the median with amounts spread
16.7×, so a rule tight enough to be safe never joins them, while a rule loose
enough to join them sweeps in tens of thousands of accounts and the
`MaxAccounts` cap then discards the result — 27 of 45 rings in the two tighter
arms. Both horns are measured, which is what makes this a conclusion rather than
a failure to find a setting.
**Alternative considered:** (A) a narrowly defined additional relationship —
rejected, no relationship was found that separates the rings from a random
control on any axis, including the structural one. (B) a separate two-part
candidate representation — rejected *for this dataset*, not in principle: the
representation is described in §20.8 and is perfectly buildable, but it needs
binding evidence to aggregate across the disconnected components and §20.7
measures every available binding at or below the level of randomly chosen
transfers. Tuning a tolerance until BIPARTITE moved — rejected, that is choosing
a setting after seeing the number, and the giant-component cost was rising
monotonically the whole way.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-28 — Record that these rings may be unlearnable, as a reading not a finding
**Chose:** state in §20.8 that IBM AML may assign BIPARTITE pairs without any
shared covariate, making the ring identifiable only by its label — and mark it
explicitly as a reading rather than a measured finding.
**Why:** it is the explanation most consistent with every number here: amounts
distinct within every ring, sender entities as distinct as the control's to
three decimals, shared counterparties *rarer* than the control. But it cannot be
falsified from inside the dataset — proving a relationship absent needs the
generator's source, not the generated ledger. Writing it as a finding would be
claiming a certainty the method cannot produce; leaving it out would omit the
most likely reason the section came out empty.
**Alternative considered:** assert it as the conclusion — rejected, unfalsifiable
from the available evidence. Omit it and report only the null results —
rejected, a reader is owed the best available explanation for six dead
hypotheses.
**My answer before seeing yours:** n/a — autonomous session, no pause requested.

## 2026-08-28 — Split the console's pipeline preparation from its ledger read
**Chose:** extract `prepareFrom(led, typologies, trainFraction)` out of
`Server.prepare`, leaving `prepare` as the database read plus a call to it, and
drive the console's tests through `prepareFrom` with a ledger built in memory.
Model tiers in those tests are small `agent.Assessor` implementations behind the
real `agent.Chain` and the real `agent.Policy`.
**Why:** `web.New` cannot be called by a test — it loads five million rows
before it does anything else — so the handlers were the largest untested surface
in the project. The split is ten lines moved and no behaviour change, and it
means the tests exercise the real detector, the real ranker, the real evaluation
and the real policy clamp over a small ledger, rather than a stand-in for any of
them. Only the two things a test cannot supply — the ledger read and the label
table — are on the other side of the seam. Faking the chain instead would have
put the policy envelope, which is the part worth protecting, inside the fake.
**Alternative considered:** (A) seed a synthetic AML dataset into a `dbtest`
schema and call the real `web.New` — rejected, it makes every console test
require Postgres to exercise arithmetic that needs none, and leaves the test
depending on tiny-ledger detection happening to produce a non-empty train and
test split. (B) build the `Server` struct directly in an in-package test file
with no production change — rejected, it restates thirty lines of `prepare` and
the copy drifts silently the first time `prepare` changes.
**My answer before seeing yours:** C — extract `prepareFrom`. (Vinayak chose the
same; the pause was asked and answered before implementation.)

## 2026-08-29 — Quote the lift over the population the detector works on
**Chose:** The performance page derives its headline lift from the run —
precision at 50 alerts over the laundering rate of the ACH-filtered ledger the
console holds — and states it as **~22× over 0.64%**, with one line saying the
raw-ledger multiple is several times larger and is deliberately not the number
being claimed.
**Why:** The page said "roughly 100×", which belonged to the `base` feature set
and sat beside a table reporting the `temporal` set's 14.32%. Deriving it stops
that drift permanently. Deriving it also exposed that the familiar 100×/140×
multiples divide a post-filter precision by a pre-filter base rate: the channel
filter drops 88% of the ledger while keeping 86.6% of the laundering, so most
of that multiple is the filter's work, not the ranker's. Quoting 22× is the
conservative reading and the one that survives being asked how it was computed.
**Alternative considered:** Keep the raw-ledger framing and simply correct 100×
to 140×. Rejected because it fixes the arithmetic while leaving the
population mismatch, and 140× is the flattering framing of the same measurement.
The precision figure itself (14.32% at 50 alerts) is unchanged either way; only
what it is stated as a multiple *of* has changed.
**My answer before seeing yours:** n/a — this surfaced mid-validation while
fixing a stale number, and was implemented before it could be raised. It is
flagged for review: reverting to "140× the 0.1% raw base rate" is a one-line
template change if the pitch should keep the larger figure.

## 2026-08-29 — Break hero-case ties on ring id, not on the map seed
**Chose:** `pickHero` sorts by transfer count and then by lowest ring id.
**Why:** The eligible set is built by ranging a map and `sort.Slice` is
unstable, so any tie at the top was resolved by Go's map seed — the console
would open on a different case per restart, and the runbook names the case.
Caught on the synthetic ledger, where two calls in one process returned ring 28
then ring 26. This is the same defect as FINDINGS §11 in a second place.
**Alternative considered:** `sort.SliceStable`. Rejected because it preserves
input order and the input order is the map's — stability over a random
permutation is still random. A total order is the only fix.
**My answer before seeing yours:** n/a — mechanical application of the tie-break
rule `detect.RankOrder` already establishes, so no pause was taken.
