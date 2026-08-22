-- WARREN schema for the IEEE-CIS Fraud Detection dataset.
--
-- Design note: the dataset has 394 transaction columns, but they are not equal.
-- A small set carries real semantic meaning (card, address, email, amount) and is
-- what entity resolution and human-readable explanations are built on, so those get
-- named, typed, indexed columns. The bulk (V1-V339, C1-C14, D1-D15, M1-M9) are
-- anonymised feature blocks that only ever move as a vector into the model, so they
-- are stored as arrays. Naming 339 opaque columns would be false precision.
--
-- Arrays are 1-indexed and positional: v_features[1] = V1, c_counts[1] = C1,
-- d_deltas[1] = D1, m_flags[1] = M1. NULL elements are preserved (they mean
-- "missing", which gradient-boosted trees consume natively as NaN).

DROP TABLE IF EXISTS identities;
DROP TABLE IF EXISTS transactions;

CREATE TABLE transactions (
    transaction_id  INTEGER       PRIMARY KEY,
    is_fraud        BOOLEAN       NOT NULL,

    -- TransactionDT is a seconds offset from an unspecified reference point,
    -- not a wall-clock timestamp. Kept raw; day/hour are derived at query time.
    transaction_dt  INTEGER       NOT NULL,

    -- NUMERIC(12,3) is exact for this data (max 31937.391, max 3 decimal places).
    -- The decimal fraction is signal, not noise: 3-decimal amounts are foreign
    -- currency conversion artifacts, so it must not be rounded away.
    transaction_amt NUMERIC(12,3) NOT NULL,

    product_cd      TEXT,

    -- Payment instrument. card1 is a large-cardinality id; card2/3/5 are small
    -- integer codes; card4/card6 are issuer and credit/debit.
    card1           INTEGER,
    card2           SMALLINT,
    card3           SMALLINT,
    card4           TEXT,
    card5           SMALLINT,
    card6           TEXT,

    -- Billing address region codes. ~11% NULL, which bounds how much of the
    -- graph can be linked on address alone.
    addr1           SMALLINT,
    addr2           SMALLINT,

    dist1           REAL,
    dist2           REAL,

    p_emaildomain   TEXT,
    r_emaildomain   TEXT,

    c_counts        REAL[],  -- C1..C14   (14 elements)
    d_deltas        REAL[],  -- D1..D15   (15 elements)
    m_flags         TEXT[],  -- M1..M9    (9 elements)
    v_features      REAL[]   -- V1..V339  (339 elements)
);

CREATE TABLE identities (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(transaction_id),

    -- id_01..id_38 are kept as named columns rather than arrays because the
    -- numeric/categorical split is interleaved, and several are directly useful
    -- for device fingerprinting and abuse signals rather than being opaque.
    id_01 REAL, id_02 REAL, id_03 REAL, id_04 REAL, id_05 REAL,
    id_06 REAL, id_07 REAL, id_08 REAL, id_09 REAL, id_10 REAL,
    id_11 REAL,
    id_12 TEXT,
    id_13 REAL, id_14 REAL,
    id_15 TEXT, id_16 TEXT,
    id_17 REAL, id_18 REAL, id_19 REAL, id_20 REAL, id_21 REAL, id_22 REAL,
    id_23 TEXT,              -- proxy/anonymiser status (IP_PROXY:HIDDEN, ...)
    id_24 REAL, id_25 REAL, id_26 REAL,
    id_27 TEXT, id_28 TEXT, id_29 TEXT,
    id_30 TEXT,              -- operating system
    id_31 TEXT,              -- browser
    id_32 REAL,              -- screen colour depth
    id_33 TEXT,              -- screen resolution
    id_34 TEXT,              -- match_status
    id_35 TEXT, id_36 TEXT, id_37 TEXT, id_38 TEXT,

    device_type TEXT,
    device_info TEXT
);

-- Indexes on the fields entity resolution links on. These are the joins the
-- ring detector runs repeatedly, so they are indexed up front rather than
-- discovered as a slow query later.
CREATE INDEX idx_txn_card1         ON transactions (card1);
CREATE INDEX idx_txn_card_entity   ON transactions (card1, card2, card3, card5, addr1);
CREATE INDEX idx_txn_p_email       ON transactions (p_emaildomain);
CREATE INDEX idx_txn_dt            ON transactions (transaction_dt);
CREATE INDEX idx_txn_is_fraud      ON transactions (is_fraud);
CREATE INDEX idx_ident_device_info ON identities (device_info);
