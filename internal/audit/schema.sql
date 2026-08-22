-- Append-only, hash-chained record of every decision the system made.
--
-- Each row's hash covers its own content and the hash before it, so the rows
-- form a chain. Editing a decision after the fact — softening a rationale,
-- changing an action, deleting an embarrassing alert — breaks every hash from
-- that point on, and the break is detectable without a copy of the original.
--
-- This is tamper-evidence, not tamper-proofing. It cannot stop someone with
-- database access from rewriting history; it makes rewritten history visible.

CREATE TABLE IF NOT EXISTS audit_log (
    seq         BIGSERIAL   PRIMARY KEY,
    decided_at  TIMESTAMPTZ NOT NULL,

    ring_id     INTEGER     NOT NULL,
    action      TEXT        NOT NULL,
    proposed    TEXT        NOT NULL,
    confidence  REAL        NOT NULL,
    source      TEXT        NOT NULL,
    rationale   TEXT        NOT NULL,

    -- Every policy intervention and every provider degradation, in order.
    adjustments TEXT[]      NOT NULL DEFAULT '{}',

    -- The exact figures the decision was made on, so it can be re-examined
    -- later without re-deriving them from a ledger that has since moved on.
    evidence    JSONB       NOT NULL,

    prev_hash   TEXT        NOT NULL,
    hash        TEXT        NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_ring ON audit_log (ring_id);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_log (decided_at);
