-- Schema for the IBM Anti-Money Laundering dataset.
--
-- This dataset supplies what IEEE-CIS could not: an explicit transfer graph
-- (every row names a sending and a receiving account), an account-to-entity
-- mapping, and a separate file labelling which transactions form which
-- laundering ring. Ring membership is therefore ground truth here rather than
-- something reconstructed and hoped for.

DROP TABLE IF EXISTS aml_pattern_txns;
DROP TABLE IF EXISTS aml_patterns;
DROP TABLE IF EXISTS aml_transactions;
DROP TABLE IF EXISTS aml_accounts;

-- Account numbers repeat across banks, so identity is the (bank, account) pair.
-- entity_id is the owning party: several accounts at several banks can belong
-- to one entity, which is exactly the structure ring detection must recover.
CREATE TABLE aml_accounts (
    bank_id        TEXT NOT NULL,
    account_number TEXT NOT NULL,
    bank_name      TEXT,
    entity_id      TEXT,
    entity_name    TEXT,
    PRIMARY KEY (bank_id, account_number)
);

-- The source has no transaction identifier, so one is assigned on load. Row
-- order in the file is stable, which keeps ids reproducible across reloads.
CREATE TABLE aml_transactions (
    txn_id             INTEGER       PRIMARY KEY,
    ts                 TIMESTAMP     NOT NULL,
    from_bank          TEXT          NOT NULL,
    from_account       TEXT          NOT NULL,
    to_bank            TEXT          NOT NULL,
    to_account         TEXT          NOT NULL,
    amount_received    NUMERIC(20,2) NOT NULL,
    receiving_currency TEXT          NOT NULL,
    amount_paid        NUMERIC(20,2) NOT NULL,
    payment_currency   TEXT          NOT NULL,
    payment_format     TEXT          NOT NULL,
    is_laundering      BOOLEAN       NOT NULL,

    -- Set during ingest by matching against the patterns file. NULL means the
    -- transaction belongs to no labelled ring, which includes laundering
    -- transactions the pattern file does not attribute to a typology.
    pattern_id         INTEGER
);

-- One labelled laundering ring, with the shape the generator used to build it.
-- Keeping the typology lets recall be reported per ring shape rather than as a
-- single average that hides which structures the detector misses entirely.
CREATE TABLE aml_patterns (
    pattern_id  INTEGER PRIMARY KEY,
    typology    TEXT    NOT NULL,
    description TEXT    NOT NULL,
    txn_count   INTEGER NOT NULL
);

-- The raw transaction lines quoted inside the patterns file, kept so the match
-- back to aml_transactions can be audited rather than trusted.
CREATE TABLE aml_pattern_txns (
    pattern_id       INTEGER NOT NULL REFERENCES aml_patterns(pattern_id),
    ts               TIMESTAMP NOT NULL,
    from_bank        TEXT NOT NULL,
    from_account     TEXT NOT NULL,
    to_bank          TEXT NOT NULL,
    to_account       TEXT NOT NULL,
    amount_paid      NUMERIC(20,2) NOT NULL,
    payment_currency TEXT NOT NULL,
    matched_txn_id   INTEGER
);

CREATE INDEX idx_aml_txn_from     ON aml_transactions (from_bank, from_account);
CREATE INDEX idx_aml_txn_to       ON aml_transactions (to_bank, to_account);
CREATE INDEX idx_aml_txn_laund    ON aml_transactions (is_laundering);
CREATE INDEX idx_aml_txn_pattern  ON aml_transactions (pattern_id);
CREATE INDEX idx_aml_txn_ts       ON aml_transactions (ts);
CREATE INDEX idx_aml_accounts_ent ON aml_accounts (entity_id);
