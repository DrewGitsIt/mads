-- This host's mesh settings as a real row (docs/MESH-NETWORK-DESIGN.md,
-- "The host's network choices"), the shape of config: one row, JSON data.
-- Not inside config, because profiles snapshot config and switching
-- profiles mustn't change where the machine listens. Until now the newest
-- device.settings_changed was the setting; that event stays, written in the
-- same transaction as the row, as its history.
CREATE TABLE mesh_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    data       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- The row, from what the machine ran with. A fresh install (nothing ever
-- meshed: no Mesh Service report, no setting, no paired or viewed device)
-- has no mesh network and listens nowhere until set up. An upgraded one is
-- already set up with Tailscale as its mesh network, so it listens exactly
-- where it did: its newest readable require_tailnet false keeps the
-- Tailscale check off, true or never set keeps it on (the check is on by
-- default, docs/MESH-NETWORK-DESIGN.md decisions, 2026-11-22).
INSERT INTO mesh_settings (id, data, updated_at)
SELECT 1,
    CASE
        WHEN NOT EXISTS (SELECT 1 FROM events WHERE type IN ('mesh.listening', 'device.settings_changed'))
         AND NOT EXISTS (SELECT 1 FROM mesh_peers)
            THEN '{"networks":[]}'
        WHEN (SELECT json_extract(payload, '$.require_tailnet') FROM events
              WHERE type = 'device.settings_changed' AND actor = 'settings'
                AND json_valid(payload) AND json_type(payload, '$.require_tailnet') IN ('true', 'false')
              ORDER BY seq DESC LIMIT 1) = 0
            THEN json_object('networks', json_array(json_object('id', 'tailscale', 'listen', json('true'),
                     'decided_at', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))),
                 'require_tailnet', json('false'))
        ELSE json_object('networks', json_array(json_object('id', 'tailscale', 'listen', json('true'), 'tailnet_check', json('true'),
                 'decided_at', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))),
             'require_tailnet', json('true'))
    END,
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now');
