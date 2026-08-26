-- Append-only, hash-chained record of every hold the system placed on an
-- account and every hold it released.
--
-- It is a second chain rather than more rows in audit_log, and the split is
-- deliberate. audit_log records what the system *decided*; this records what it
-- *did about it*. Folding both into one table would mean either leaving the
-- record type outside the hash, which makes it forgeable, or bringing it inside
-- and invalidating the hash of every decision already written. Two chains, each
-- internally consistent, with every action naming the decision that authorised
-- it, keeps both properties and loses nothing: a lease whose decision_seq points
-- at a broken audit entry is money being held on an authority that no longer
-- verifies, and that join is the whole point of recording it this way.
--
-- Rows are events, never updated. Current state is the fold of the events, so
-- "this account was frozen for 40 hours and then released" survives, which a
-- table of current state cannot say.

CREATE TABLE IF NOT EXISTS account_restrictions (
    seq          BIGSERIAL   PRIMARY KEY,
    event        TEXT        NOT NULL,   -- 'impose' | 'lift'
    recorded_at  TIMESTAMPTZ NOT NULL,

    account      TEXT        NOT NULL,   -- "bank|account", stable across runs
    tier         TEXT        NOT NULL,   -- 'frozen' | 'watch'
    ring_id      INTEGER     NOT NULL,

    -- The audit entry that authorised this. Null only for a lift performed by a
    -- person, which is authorised by the person, not by a decision.
    decision_seq BIGINT,

    -- Which detection geometry raised the ring. A 24h read and a 72h read carry
    -- different evidential weight and the lease length differs accordingly, so a
    -- record that cannot say which one imposed it cannot justify its own length.
    pass         TEXT        NOT NULL DEFAULT '',
    reason       TEXT        NOT NULL,

    imposed_at   TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,

    prev_hash    TEXT        NOT NULL,
    hash         TEXT        NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_restrict_account  ON account_restrictions (account);
CREATE INDEX IF NOT EXISTS idx_restrict_decision ON account_restrictions (decision_seq);
CREATE INDEX IF NOT EXISTS idx_restrict_expiry   ON account_restrictions (expires_at);
