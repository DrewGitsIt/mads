-- meta is a key-value table for data-directory facts that aren't user state:
-- schema provenance, install stamps, anything the update lifecycle (spec §14)
-- needs to reason about a data dir it didn't create.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
