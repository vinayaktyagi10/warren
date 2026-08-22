# WARREN

Coordinated abuse-ring detection for card-not-present payments.

A rabbit warren looks like separate burrows from the surface. WARREN finds the
tunnels: sets of apparently unrelated customer identities that are really one
actor operating behind shared cards, addresses and devices — the loss class that
per-transaction fraud scoring structurally cannot see, because every individual
transaction in a ring can look ordinary.

## Status

Under construction. Currently: dataset ingest.

## Data

[IEEE-CIS Fraud Detection](https://www.kaggle.com/c/ieee-fraud-detection)
(Vesta Corporation / IEEE Computational Intelligence Society): 590,540 real
card-not-present transactions, 394 features, 3.499% fraud rate, with a joined
device/identity table covering 144,233 of them.

The dataset ships no ring labels — only a per-transaction `isFraud` flag. Ring
membership has to be reconstructed from shared identity fields, and that
reconstruction is the detector.

## Running it

Requires Go 1.24+, Docker, and a Kaggle account that has accepted the
competition rules.

```sh
docker compose up -d                       # Postgres on :5432

python3 -m venv .venv && .venv/bin/pip install kaggle
.venv/bin/kaggle competitions download -c ieee-fraud-detection -p data/
unzip data/ieee-fraud-detection.zip -d data/

go run ./cmd/ingest -data data             # ~65s, streams via COPY
```

Ingest applies the schema from scratch each run, so it is safe to re-run.

It finishes by printing the figures that prove the load is faithful:

```
transactions=590540 frauds=20663 rate=3.4990% identities=144233 amt=[0.251..31937.391]
```

Those match the published dataset exactly. If they drift, rows were dropped or
mangled on the way in.

## Layout

```
cmd/ingest/        CSV -> Postgres via the COPY protocol
internal/db/       connection handling and schema
data/              raw CSVs (gitignored)
```

## Configuration

`DATABASE_URL` overrides the local development database.
