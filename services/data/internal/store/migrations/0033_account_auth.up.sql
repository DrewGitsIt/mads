-- Provider login sign-in state (docs/AUTH-SIGNIN-DESIGN.md): one row per
-- login (provider.AccountStatus.Account), created by its first auth failure.
-- Signed out means failed_at is after signed_in_at; the store compares the
-- two in Go, since RFC 3339 with variable fractional digits doesn't sort as
-- text. failed_session lets the Router find the login of a session whose
-- record doesn't name one yet.
CREATE TABLE account_auth (
    account         TEXT PRIMARY KEY,
    provider        TEXT NOT NULL,
    failed_at       TEXT NOT NULL DEFAULT '',
    failed_session  TEXT NOT NULL DEFAULT '',
    signed_in_at    TEXT NOT NULL DEFAULT ''
);
