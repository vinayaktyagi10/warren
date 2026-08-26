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
