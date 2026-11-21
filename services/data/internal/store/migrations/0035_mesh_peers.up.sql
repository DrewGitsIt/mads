-- Mesh mode (docs/MESH-MODE-DESIGN.md D3): the devices this node views.
-- A registry table like workspaces: the Peer JSON lives in data, name is
-- the unique label (and the Router's /peers/{peer}/ proxy segment).
CREATE TABLE mesh_peers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    system     INTEGER NOT NULL DEFAULT 0,
    data       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
