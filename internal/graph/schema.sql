-- Entity resolution output: clients, the rings they fall into, and the evidence
-- for every link. Rebuilt from scratch on each run of cmd/rings.

DROP TABLE IF EXISTS ring_links;
DROP TABLE IF EXISTS transaction_clients;
DROP TABLE IF EXISTS clients;

-- A client is a resolved account: the finest unit that plausibly corresponds to
-- one real cardholder. ring_id is NULL when nothing links this client to another,
-- which is the common case and is not suspicious on its own.
CREATE TABLE clients (
    client_id  INTEGER PRIMARY KEY,
    client_key TEXT    NOT NULL UNIQUE,
    ring_id    INTEGER
);

CREATE TABLE transaction_clients (
    transaction_id INTEGER PRIMARY KEY REFERENCES transactions(transaction_id),
    client_id      INTEGER NOT NULL REFERENCES clients(client_id)
);

-- Why two clients were joined. Every ring must be explainable down to the shared
-- value that caused the link, otherwise a flagged ring is an unaccountable
-- assertion and cannot justify holding someone's money.
CREATE TABLE ring_links (
    ring_id     INTEGER NOT NULL,
    rule        TEXT    NOT NULL,
    link_value  TEXT    NOT NULL,
    client_count INTEGER NOT NULL
);

CREATE INDEX idx_clients_ring     ON clients (ring_id);
CREATE INDEX idx_txn_clients_cid  ON transaction_clients (client_id);
CREATE INDEX idx_ring_links_ring  ON ring_links (ring_id);
