package store

// migrate creates all tables, indexes, and append-only triggers.
// Idempotent — safe to call on every startup.
func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

// schema defines the full database structure.
//
// Design principles:
//  1. Ledger tables are APPEND-ONLY. Triggers block UPDATE and DELETE.
//  2. Corrections are compensating entries (like real accounting).
//  3. Balances are derived by summing events, never stored directly.
//  4. Every payout traces back to settlement entries → usage reports → tickets → invoices.
const schema = `
-- ============================================================
-- INVOICES: Lightning payment requests issued by the Hub
-- ============================================================
CREATE TABLE IF NOT EXISTS invoices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    payment_hash    TEXT    NOT NULL UNIQUE,
    payment_request TEXT    NOT NULL,
    amount_sats     INTEGER NOT NULL CHECK (amount_sats > 0),
    tier            TEXT    NOT NULL,
    bytes_allowed   INTEGER NOT NULL CHECK (bytes_allowed > 0),
    status          TEXT    NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'settled', 'expired')),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    settled_at      TEXT,
    expires_at      TEXT    NOT NULL,
    client_ip       TEXT
);

CREATE INDEX IF NOT EXISTS idx_invoices_status ON invoices(status);
CREATE INDEX IF NOT EXISTS idx_invoices_payment_hash ON invoices(payment_hash);

-- Append-only: no rewrites of invoice history.
CREATE TRIGGER IF NOT EXISTS invoices_no_delete
BEFORE DELETE ON invoices
BEGIN
    SELECT RAISE(FAIL, 'invoices is append-only: deletions are prohibited');
END;

-- Invoices: only status and settled_at may change, and only via forward transitions.
CREATE TRIGGER IF NOT EXISTS invoices_immutable_fields
BEFORE UPDATE ON invoices
WHEN OLD.payment_hash != NEW.payment_hash
  OR OLD.amount_sats != NEW.amount_sats
  OR OLD.tier != NEW.tier
  OR OLD.bytes_allowed != NEW.bytes_allowed
  OR OLD.payment_request != NEW.payment_request
BEGIN
    SELECT RAISE(FAIL, 'invoice financial fields are immutable');
END;

-- Invoices: enforce forward-only status transitions (open→settled, open→expired).
CREATE TRIGGER IF NOT EXISTS invoices_forward_transition
BEFORE UPDATE ON invoices
WHEN OLD.status != 'open' AND OLD.status != NEW.status
BEGIN
    SELECT RAISE(FAIL, 'invoice status transitions are forward-only');
END;

-- ============================================================
-- TICKETS: Bandwidth credentials issued after payment
-- Each ticket is atomic (fully consumed or not) and single-use.
-- ============================================================
CREATE TABLE IF NOT EXISTS tickets (
    id              TEXT    PRIMARY KEY,
    payment_hash    TEXT    NOT NULL REFERENCES invoices(payment_hash),
    bytes_value     INTEGER NOT NULL CHECK (bytes_value > 0),
    hmac            TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'redeemed')),
    issued_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    redeemed_at     TEXT,
    redeemed_by     TEXT
);

CREATE INDEX IF NOT EXISTS idx_tickets_payment_hash ON tickets(payment_hash);
CREATE INDEX IF NOT EXISTS idx_tickets_status ON tickets(status);

-- Append-only: tickets are never deleted.
CREATE TRIGGER IF NOT EXISTS tickets_no_delete
BEFORE DELETE ON tickets
BEGIN
    SELECT RAISE(FAIL, 'tickets is append-only: deletions are prohibited');
END;

-- Tickets can only transition forward: active → redeemed.
-- Prevent any other status change or field tampering after redemption.
CREATE TRIGGER IF NOT EXISTS tickets_no_rewrite
BEFORE UPDATE ON tickets
WHEN OLD.status = 'redeemed'
BEGIN
    SELECT RAISE(FAIL, 'redeemed tickets cannot be modified');
END;

-- Tickets: financial fields are immutable even while active.
CREATE TRIGGER IF NOT EXISTS tickets_immutable_fields
BEFORE UPDATE ON tickets
WHEN OLD.bytes_value != NEW.bytes_value
  OR OLD.hmac != NEW.hmac
  OR OLD.payment_hash != NEW.payment_hash
BEGIN
    SELECT RAISE(FAIL, 'ticket financial fields are immutable');
END;

-- ============================================================
-- USAGE REPORTS: Signed bandwidth attestations from nodes
-- Nodes submit these periodically. Hub uses them for settlement.
-- ============================================================
CREATE TABLE IF NOT EXISTS usage_reports (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT    NOT NULL,
    ticket_id       TEXT    NOT NULL REFERENCES tickets(id),
    node_pubkey     TEXT    NOT NULL,
    node_role       TEXT    NOT NULL CHECK (node_role IN ('entry', 'exit')),
    bytes_reported  INTEGER NOT NULL CHECK (bytes_reported >= 0),
    reported_at     TEXT    NOT NULL,
    node_signature  TEXT    NOT NULL,
    received_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_usage_reports_session ON usage_reports(session_id);
CREATE INDEX IF NOT EXISTS idx_usage_reports_ticket ON usage_reports(ticket_id);
CREATE INDEX IF NOT EXISTS idx_usage_reports_node ON usage_reports(node_pubkey);

-- Append-only: usage reports are immutable evidence.
CREATE TRIGGER IF NOT EXISTS usage_reports_no_update
BEFORE UPDATE ON usage_reports
BEGIN
    SELECT RAISE(FAIL, 'usage_reports is append-only: updates are prohibited');
END;

CREATE TRIGGER IF NOT EXISTS usage_reports_no_delete
BEFORE DELETE ON usage_reports
BEGIN
    SELECT RAISE(FAIL, 'usage_reports is append-only: deletions are prohibited');
END;

-- ============================================================
-- SETTLEMENT ENTRIES: Calculated payouts per node per period
-- Derived from usage reports. Idempotent by (period, node).
-- ============================================================
CREATE TABLE IF NOT EXISTS settlement_entries (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    settlement_period   TEXT    NOT NULL,
    node_pubkey         TEXT    NOT NULL,
    billable_bytes      INTEGER NOT NULL CHECK (billable_bytes >= 0),
    amount_sats         INTEGER NOT NULL CHECK (amount_sats >= 0),
    entry_bytes_total   INTEGER NOT NULL,
    exit_bytes_total    INTEGER NOT NULL,
    tickets_redeemed    INTEGER NOT NULL,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE(settlement_period, node_pubkey)
);

CREATE INDEX IF NOT EXISTS idx_settlement_period ON settlement_entries(settlement_period);
CREATE INDEX IF NOT EXISTS idx_settlement_node ON settlement_entries(node_pubkey);

-- Append-only: settlement history is sacrosanct.
CREATE TRIGGER IF NOT EXISTS settlement_entries_no_update
BEFORE UPDATE ON settlement_entries
BEGIN
    SELECT RAISE(FAIL, 'settlement_entries is append-only: updates are prohibited');
END;

CREATE TRIGGER IF NOT EXISTS settlement_entries_no_delete
BEFORE DELETE ON settlement_entries
BEGIN
    SELECT RAISE(FAIL, 'settlement_entries is append-only: deletions are prohibited');
END;

-- ============================================================
-- PAYOUTS: Lightning payments made to nodes
-- State machine: pending → in_flight → paid | failed → retrying → in_flight → paid | failed
-- ============================================================
CREATE TABLE IF NOT EXISTS payouts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    settlement_entry_id INTEGER NOT NULL REFERENCES settlement_entries(id),
    node_pubkey         TEXT    NOT NULL,
    amount_sats         INTEGER NOT NULL CHECK (amount_sats > 0),
    status              TEXT    NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'in_flight', 'paid', 'failed', 'retrying')),
    payment_hash        TEXT,
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    created_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_payouts_status ON payouts(status);
CREATE INDEX IF NOT EXISTS idx_payouts_node ON payouts(node_pubkey);

-- One payout per settlement entry — prevents duplicate payout creation.
DROP INDEX IF EXISTS idx_payouts_settlement;
CREATE UNIQUE INDEX IF NOT EXISTS idx_payouts_settlement_unique ON payouts(settlement_entry_id);

-- Payouts are never deleted.
CREATE TRIGGER IF NOT EXISTS payouts_no_delete
BEFORE DELETE ON payouts
BEGIN
    SELECT RAISE(FAIL, 'payouts is append-only: deletions are prohibited');
END;

-- Payouts: financial fields are immutable once created.
-- Only status, payment_hash, attempt_count, last_error, and updated_at may change.
CREATE TRIGGER IF NOT EXISTS payouts_immutable_fields
BEFORE UPDATE ON payouts
WHEN OLD.amount_sats != NEW.amount_sats
  OR OLD.node_pubkey != NEW.node_pubkey
  OR OLD.settlement_entry_id != NEW.settlement_entry_id
BEGIN
    SELECT RAISE(FAIL, 'payout financial fields are immutable');
END;

-- ============================================================
-- COMPENSATING ENTRIES: Corrections to the ledger
-- When something is wrong, we don't edit — we post a correction.
-- ============================================================
CREATE TABLE IF NOT EXISTS compensating_entries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_type      TEXT    NOT NULL CHECK (entry_type IN ('settlement', 'payout', 'ticket')),
    reference_id    INTEGER NOT NULL,
    adjustment_sats INTEGER NOT NULL,
    reason          TEXT    NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Compensating entries are themselves append-only.
CREATE TRIGGER IF NOT EXISTS compensating_no_update
BEFORE UPDATE ON compensating_entries
BEGIN
    SELECT RAISE(FAIL, 'compensating_entries is append-only: updates are prohibited');
END;

CREATE TRIGGER IF NOT EXISTS compensating_no_delete
BEFORE DELETE ON compensating_entries
BEGIN
    SELECT RAISE(FAIL, 'compensating_entries is append-only: deletions are prohibited');
END;

-- ============================================================
-- ENTITLEMENTS: Track redeemable token quotas per purchase
-- Created when an invoice is settled. Decremented when client
-- redeems tokens via POST /redeem.
-- ============================================================
CREATE TABLE IF NOT EXISTS entitlements (
    id               TEXT    PRIMARY KEY,
    payment_hash     TEXT    NOT NULL UNIQUE REFERENCES invoices(payment_hash),
    tokens_remaining INTEGER NOT NULL CHECK (tokens_remaining >= 0),
    tokens_total     INTEGER NOT NULL CHECK (tokens_total > 0),
    bytes_per_token  INTEGER NOT NULL CHECK (bytes_per_token > 0),
    key_id           TEXT    NOT NULL,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_entitlements_payment ON entitlements(payment_hash);

-- Entitlements: no deletion.
CREATE TRIGGER IF NOT EXISTS entitlements_no_delete
BEFORE DELETE ON entitlements
BEGIN
    SELECT RAISE(FAIL, 'entitlements is append-only: deletions are prohibited');
END;

-- Entitlements: immutable fields (only tokens_remaining may change).
CREATE TRIGGER IF NOT EXISTS entitlements_immutable_fields
BEFORE UPDATE ON entitlements
WHEN OLD.payment_hash != NEW.payment_hash
  OR OLD.tokens_total != NEW.tokens_total
  OR OLD.bytes_per_token != NEW.bytes_per_token
  OR OLD.key_id != NEW.key_id
BEGIN
    SELECT RAISE(FAIL, 'entitlement financial fields are immutable');
END;

-- ============================================================
-- SPENT TOKENS: Double-spend prevention for blind-signed tokens
-- Atomic INSERT is the sole correctness primitive.
-- ============================================================
CREATE TABLE IF NOT EXISTS spent_tokens (
    token_id    TEXT PRIMARY KEY,
    key_id      TEXT NOT NULL,
    node_pubkey TEXT NOT NULL,
    spent_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Spent tokens are never deleted or modified.
CREATE TRIGGER IF NOT EXISTS spent_tokens_no_delete
BEFORE DELETE ON spent_tokens
BEGIN
    SELECT RAISE(FAIL, 'spent_tokens is append-only: deletions are prohibited');
END;

CREATE TRIGGER IF NOT EXISTS spent_tokens_no_update
BEFORE UPDATE ON spent_tokens
BEGIN
    SELECT RAISE(FAIL, 'spent_tokens is append-only: updates are prohibited');
END;

-- ============================================================
-- REDEMPTIONS: Idempotent /redeem response cache
-- Ensures crash-safe blind signature delivery.
-- ============================================================
CREATE TABLE IF NOT EXISTS redemptions (
    nonce           TEXT PRIMARY KEY,
    entitlement_id  TEXT    NOT NULL REFERENCES entitlements(id),
    request_hash    TEXT    NOT NULL,
    tokens_count    INTEGER NOT NULL CHECK (tokens_count > 0),
    blind_signatures TEXT   NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Redemptions are never deleted or modified.
CREATE TRIGGER IF NOT EXISTS redemptions_no_delete
BEFORE DELETE ON redemptions
BEGIN
    SELECT RAISE(FAIL, 'redemptions is append-only: deletions are prohibited');
END;

CREATE TRIGGER IF NOT EXISTS redemptions_no_update
BEFORE UPDATE ON redemptions
BEGIN
    SELECT RAISE(FAIL, 'redemptions is append-only: updates are prohibited');
END;

-- ============================================================
-- NODE LEASES: Authorization windows for node attestation refresh
-- A node can only refresh its attestation while its lease is active.
-- ============================================================
CREATE TABLE IF NOT EXISTS node_leases (
    node_pubkey     TEXT PRIMARY KEY,
    node_wg_pubkey  TEXT    NOT NULL,
    operator_id     TEXT    NOT NULL,
    allowed_roles   TEXT    NOT NULL,
    lease_start     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    lease_end       TEXT    NOT NULL,
    revoked         INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- ============================================================
-- NODE WALLETS: Payout destinations for node operators
-- Nodes register a Lightning address for receiving earnings.
-- ============================================================
CREATE TABLE IF NOT EXISTS node_wallets (
    node_pubkey     TEXT PRIMARY KEY,
    ln_address      TEXT    NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- ============================================================
-- WITHDRAWALS: Track node operator withdrawal requests
-- ============================================================
CREATE TABLE IF NOT EXISTS withdrawals (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    node_pubkey     TEXT    NOT NULL,
    amount_sats     INTEGER NOT NULL CHECK (amount_sats > 0),
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'paid', 'failed')),
    payment_hash    TEXT,
    last_error      TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_withdrawals_node ON withdrawals(node_pubkey);
CREATE INDEX IF NOT EXISTS idx_withdrawals_status ON withdrawals(status);
`
