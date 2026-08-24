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
