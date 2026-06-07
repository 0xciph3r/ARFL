package store

import (
	"database/sql"
	"fmt"
	"time"
)

// --- Invoice operations (insert + status transition only) ---

// InsertInvoice records a new Lightning invoice.
func (s *Store) InsertInvoice(paymentHash, paymentRequest string, amountSats int64, tier string, bytesAllowed int64, expiresAt time.Time, clientIP string) error {
	_, err := s.db.Exec(`
		INSERT INTO invoices (payment_hash, payment_request, amount_sats, tier, bytes_allowed, expires_at, client_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		paymentHash, paymentRequest, amountSats, tier, bytesAllowed,
		expiresAt.UTC().Format(time.RFC3339), clientIP,
	)
	return err
}

// SettleInvoice marks an invoice as settled. Idempotent: settling an
// already-settled invoice returns nil (crash-safe for retry scenarios).
// Returns an error only if the invoice doesn't exist or is in a
// non-settleable state (e.g., expired).
func (s *Store) SettleInvoice(paymentHash string) error {
	res, err := s.db.Exec(`
		UPDATE invoices SET status = 'settled', settled_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE payment_hash = ? AND status = 'open'`,
		paymentHash,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Distinguish "already settled" (idempotent) from "not found" or "expired".
		var status string
		err := s.db.QueryRow(`SELECT status FROM invoices WHERE payment_hash = ?`, paymentHash).Scan(&status)
		if err != nil {
			return fmt.Errorf("invoice %s not found", paymentHash)
		}
		if status == "settled" {
			return nil // idempotent — already settled
		}
		return fmt.Errorf("invoice %s cannot be settled (status: %s)", paymentHash, status)
	}
	return nil
}

// ExpireInvoice marks an invoice as expired. Only transitions open → expired.
func (s *Store) ExpireInvoice(paymentHash string) error {
	_, err := s.db.Exec(`
		UPDATE invoices SET status = 'expired'
		WHERE payment_hash = ? AND status = 'open'`,
		paymentHash,
	)
	return err
}

// InvoiceRecord is the read model for an invoice.
type InvoiceRecord struct {
	PaymentHash    string
	PaymentRequest string
	AmountSats     int64
	Tier           string
	BytesAllowed   int64
	Status         string
	CreatedAt      string
	SettledAt      sql.NullString
	ExpiresAt      string
}

// GetInvoice retrieves an invoice by payment hash.
func (s *Store) GetInvoice(paymentHash string) (*InvoiceRecord, error) {
	row := s.db.QueryRow(`
		SELECT payment_hash, payment_request, amount_sats, tier, bytes_allowed,
		       status, created_at, settled_at, expires_at
		FROM invoices WHERE payment_hash = ?`, paymentHash)

	var inv InvoiceRecord
	err := row.Scan(&inv.PaymentHash, &inv.PaymentRequest, &inv.AmountSats,
		&inv.Tier, &inv.BytesAllowed, &inv.Status, &inv.CreatedAt,
		&inv.SettledAt, &inv.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// --- Ticket operations (insert + redeem only) ---

// InsertTicket records a newly issued ticket.
func (s *Store) InsertTicket(id, paymentHash string, bytesValue int64, hmac string) error {
	_, err := s.db.Exec(`
		INSERT INTO tickets (id, payment_hash, bytes_value, hmac)
		VALUES (?, ?, ?, ?)`,
		id, paymentHash, bytesValue, hmac,
	)
	return err
}

// RedeemTicket marks a ticket as redeemed. Only transitions active → redeemed.
// Returns an error if the ticket is already redeemed (enforces single-use).
func (s *Store) RedeemTicket(ticketID, redeemedBy string) error {
	res, err := s.db.Exec(`
		UPDATE tickets SET status = 'redeemed',
		       redeemed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
		       redeemed_by = ?
		WHERE id = ? AND status = 'active'`,
		redeemedBy, ticketID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ticket %s not found or already redeemed", ticketID)
	}
	return nil
}

// TicketRecord is the read model for a ticket.
type TicketRecord struct {
	ID          string
	PaymentHash string
	BytesValue  int64
	HMAC        string
	Status      string
	IssuedAt    string
	RedeemedAt  sql.NullString
	RedeemedBy  sql.NullString
}

// GetTicket retrieves a ticket by ID.
func (s *Store) GetTicket(id string) (*TicketRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, payment_hash, bytes_value, hmac, status, issued_at, redeemed_at, redeemed_by
		FROM tickets WHERE id = ?`, id)

	var t TicketRecord
	err := row.Scan(&t.ID, &t.PaymentHash, &t.BytesValue, &t.HMAC,
		&t.Status, &t.IssuedAt, &t.RedeemedAt, &t.RedeemedBy)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CountTicketsByPaymentHash returns how many tickets exist for a given invoice.
func (s *Store) CountTicketsByPaymentHash(paymentHash string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tickets WHERE payment_hash = ?`, paymentHash).Scan(&count)
	return count, err
}

// GetTicketsByPaymentHash returns all tickets issued for a given invoice.
func (s *Store) GetTicketsByPaymentHash(paymentHash string) ([]TicketRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, payment_hash, bytes_value, hmac, status, issued_at, redeemed_at, redeemed_by
		FROM tickets WHERE payment_hash = ?
		ORDER BY issued_at`, paymentHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tickets []TicketRecord
	for rows.Next() {
		var t TicketRecord
		if err := rows.Scan(&t.ID, &t.PaymentHash, &t.BytesValue, &t.HMAC,
			&t.Status, &t.IssuedAt, &t.RedeemedAt, &t.RedeemedBy); err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, rows.Err()
}

// --- Usage report operations (insert-only) ---

// InsertUsageReport appends a signed usage report from a node.
func (s *Store) InsertUsageReport(sessionID, ticketID, nodePubkey, nodeRole string, bytesReported int64, reportedAt, nodeSignature string) error {
	_, err := s.db.Exec(`
		INSERT INTO usage_reports (session_id, ticket_id, node_pubkey, node_role, bytes_reported, reported_at, node_signature)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, ticketID, nodePubkey, nodeRole, bytesReported, reportedAt, nodeSignature,
	)
	return err
}

// UsageReportRecord is the read model for a usage report.
type UsageReportRecord struct {
	ID            int64
	SessionID     string
	TicketID      string
	NodePubkey    string
	NodeRole      string
	BytesReported int64
	ReportedAt    string
	NodeSignature string
	ReceivedAt    string
}

// GetUsageReportsByPeriod returns all usage reports received within a time window.
func (s *Store) GetUsageReportsByPeriod(from, to string) ([]UsageReportRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, ticket_id, node_pubkey, node_role, bytes_reported,
		       reported_at, node_signature, received_at
		FROM usage_reports
		WHERE received_at >= ? AND received_at < ?
		ORDER BY received_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reports []UsageReportRecord
	for rows.Next() {
		var r UsageReportRecord
		if err := rows.Scan(&r.ID, &r.SessionID, &r.TicketID, &r.NodePubkey,
			&r.NodeRole, &r.BytesReported, &r.ReportedAt, &r.NodeSignature,
			&r.ReceivedAt); err != nil {
			return nil, err
		}
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

// --- Settlement operations (insert-only, idempotent) ---

// InsertSettlementEntry records a settlement calculation. Idempotent by (period, node).
// Uses INSERT OR IGNORE — running settlement twice for the same period is a no-op.
// Returns true if a new row was inserted, false if it already existed.
func (s *Store) InsertSettlementEntry(period, nodePubkey string, billableBytes, amountSats, entryBytesTotal, exitBytesTotal int64, ticketsRedeemed int) (bool, error) {
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO settlement_entries
		(settlement_period, node_pubkey, billable_bytes, amount_sats,
		 entry_bytes_total, exit_bytes_total, tickets_redeemed)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		period, nodePubkey, billableBytes, amountSats,
		entryBytesTotal, exitBytesTotal, ticketsRedeemed,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SettlementEntryRecord is the read model for a settlement entry.
type SettlementEntryRecord struct {
	ID               int64
	SettlementPeriod string
	NodePubkey       string
	BillableBytes    int64
	AmountSats       int64
	EntryBytesTotal  int64
	ExitBytesTotal   int64
	TicketsRedeemed  int
	CreatedAt        string
}

// GetUnsettledEntries returns settlement entries that don't have any payout yet.
// Excludes entries that already have a payout in any state (including failed).
// Failed payouts are retried via GetRetryablePayouts, not by creating new ones.
func (s *Store) GetUnsettledEntries(minAmountSats int64) ([]SettlementEntryRecord, error) {
	rows, err := s.db.Query(`
		SELECT se.id, se.settlement_period, se.node_pubkey, se.billable_bytes,
		       se.amount_sats, se.entry_bytes_total, se.exit_bytes_total,
		       se.tickets_redeemed, se.created_at
		FROM settlement_entries se
		LEFT JOIN payouts p ON p.settlement_entry_id = se.id
		WHERE p.id IS NULL AND se.amount_sats >= ?
		ORDER BY se.created_at`, minAmountSats)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []SettlementEntryRecord
	for rows.Next() {
		var e SettlementEntryRecord
		if err := rows.Scan(&e.ID, &e.SettlementPeriod, &e.NodePubkey,
			&e.BillableBytes, &e.AmountSats, &e.EntryBytesTotal,
			&e.ExitBytesTotal, &e.TicketsRedeemed, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Payout operations (insert + state transitions) ---

// InsertPayout creates a pending payout for a settlement entry.
func (s *Store) InsertPayout(settlementEntryID int64, nodePubkey string, amountSats int64) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO payouts (settlement_entry_id, node_pubkey, amount_sats)
		VALUES (?, ?, ?)`,
		settlementEntryID, nodePubkey, amountSats,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkPayoutInFlight transitions a payout to in_flight before sending payment.
// This is the crash-safety boundary: if we crash after this, we know a payment
// was attempted and must be reconciled before retrying.
// Returns an error if the payout is not in an eligible state (pending or retrying).
func (s *Store) MarkPayoutInFlight(payoutID int64) error {
	res, err := s.db.Exec(`
		UPDATE payouts SET status = 'in_flight',
		       updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND status IN ('pending', 'retrying')`,
		payoutID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("payout %d: cannot transition to in_flight (not in pending or retrying state)", payoutID)
	}
	return nil
}

// MarkPayoutPaid transitions a payout to paid status.
// Returns an error if the payout is not in in_flight state.
func (s *Store) MarkPayoutPaid(payoutID int64, paymentHash string) error {
	res, err := s.db.Exec(`
		UPDATE payouts SET status = 'paid', payment_hash = ?,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND status = 'in_flight'`,
		paymentHash, payoutID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("payout %d: cannot transition to paid (not in in_flight state)", payoutID)
	}
	return nil
}

// MarkPayoutFailed transitions a payout to failed status.
// Returns an error if the payout is not in in_flight state.
func (s *Store) MarkPayoutFailed(payoutID int64, lastError string) error {
	res, err := s.db.Exec(`
		UPDATE payouts SET status = 'failed', last_error = ?,
		       attempt_count = attempt_count + 1,
		       updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND status = 'in_flight'`,
		lastError, payoutID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("payout %d: cannot transition to failed (not in in_flight state)", payoutID)
	}
	return nil
}

// MarkPayoutRetrying transitions a failed payout back to retrying.
func (s *Store) MarkPayoutRetrying(payoutID int64) error {
	res, err := s.db.Exec(`
		UPDATE payouts SET status = 'retrying',
		       updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND status = 'failed' AND attempt_count < 3`,
		payoutID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("payout %d not eligible for retry (not failed or max attempts reached)", payoutID)
	}
	return nil
}

// GetRetryablePayouts returns failed payouts that haven't exceeded max retries.
func (s *Store) GetRetryablePayouts() ([]PayoutRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, settlement_entry_id, node_pubkey, amount_sats, status,
		       payment_hash, attempt_count, last_error, created_at
		FROM payouts
		WHERE status = 'failed' AND attempt_count < 3
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payouts []PayoutRecord
	for rows.Next() {
		var p PayoutRecord
		if err := rows.Scan(&p.ID, &p.SettlementEntryID, &p.NodePubkey,
			&p.AmountSats, &p.Status, &p.PaymentHash, &p.AttemptCount,
			&p.LastError, &p.CreatedAt); err != nil {
			return nil, err
		}
		payouts = append(payouts, p)
	}
	return payouts, rows.Err()
}

// PayoutRecord is the read model for a payout.
type PayoutRecord struct {
	ID                int64
	SettlementEntryID int64
	NodePubkey        string
	AmountSats        int64
	Status            string
	PaymentHash       sql.NullString
	AttemptCount      int
	LastError         sql.NullString
	CreatedAt         string
}

// --- Compensating entries (append-only corrections) ---

// InsertCompensatingEntry records a correction to the ledger.
func (s *Store) InsertCompensatingEntry(entryType string, referenceID int64, adjustmentSats int64, reason string) error {
	_, err := s.db.Exec(`
		INSERT INTO compensating_entries (entry_type, reference_id, adjustment_sats, reason)
		VALUES (?, ?, ?, ?)`,
		entryType, referenceID, adjustmentSats, reason,
	)
	return err
}

// --- Audit queries ---

// TotalPurchasedSats returns the sum of all settled invoices.
func (s *Store) TotalPurchasedSats() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(amount_sats) FROM invoices WHERE status = 'settled'`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// TotalPaidOutSats returns the sum of all successful payouts.
func (s *Store) TotalPaidOutSats() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(amount_sats) FROM payouts WHERE status = 'paid'`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// TotalCompensationSats returns the net sum of all compensating entries.
func (s *Store) TotalCompensationSats() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`SELECT SUM(adjustment_sats) FROM compensating_entries`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// TotalCommittedPayoutSats returns the sum of all non-failed payouts
// (paid + pending + in_flight + retrying). Used for pre-flight budget checks.
func (s *Store) TotalCommittedPayoutSats() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`
		SELECT SUM(amount_sats) FROM payouts
		WHERE status IN ('paid', 'pending', 'in_flight', 'retrying')
	`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// --- Settlement-specific queries ---

// SessionUsageSummary is the aggregated usage for one session.
type SessionUsageSummary struct {
	SessionID  string
	TicketID   string
	EntryNode  string
	ExitNode   string
	EntryBytes int64
	ExitBytes  int64
}

// GetSessionUsageSummaries returns aggregated usage reports grouped by session,
// using MAX(bytes_reported) per role per session (cumulative reporting model).
// Only returns sessions that have BOTH entry and exit reports.
func (s *Store) GetSessionUsageSummaries(from, to string) ([]SessionUsageSummary, error) {
	rows, err := s.db.Query(`
		SELECT
			e.session_id,
			e.ticket_id,
			e.node_pubkey AS entry_node,
			x.node_pubkey AS exit_node,
			e.max_bytes   AS entry_bytes,
			x.max_bytes   AS exit_bytes
		FROM (
			SELECT session_id, ticket_id, node_pubkey, MAX(bytes_reported) AS max_bytes
			FROM usage_reports
			WHERE node_role = 'entry' AND received_at >= ? AND received_at < ?
			GROUP BY session_id, ticket_id, node_pubkey
		) e
		INNER JOIN (
			SELECT session_id, node_pubkey, MAX(bytes_reported) AS max_bytes
			FROM usage_reports
			WHERE node_role = 'exit' AND received_at >= ? AND received_at < ?
			GROUP BY session_id, node_pubkey
		) x ON e.session_id = x.session_id`,
		from, to, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []SessionUsageSummary
	for rows.Next() {
		var s SessionUsageSummary
		if err := rows.Scan(&s.SessionID, &s.TicketID, &s.EntryNode,
			&s.ExitNode, &s.EntryBytes, &s.ExitBytes); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// TicketSettlementInfo is the validated chain: ticket → invoice → tier.
type TicketSettlementInfo struct {
	TicketID      string
	TicketStatus  string
	TicketBytes   int64
	InvoiceStatus string
	AmountSats    int64
	Tier          string
}

// GetTicketSettlementInfo validates the full chain from ticket to invoice.
// Returns error if the ticket doesn't exist.
func (s *Store) GetTicketSettlementInfo(ticketID string) (*TicketSettlementInfo, error) {
	row := s.db.QueryRow(`
		SELECT t.id, t.status, t.bytes_value, i.status, i.amount_sats, i.tier
		FROM tickets t
		JOIN invoices i ON t.payment_hash = i.payment_hash
		WHERE t.id = ?`, ticketID)

	var info TicketSettlementInfo
	err := row.Scan(&info.TicketID, &info.TicketStatus, &info.TicketBytes,
		&info.InvoiceStatus, &info.AmountSats, &info.Tier)
	if err != nil {
		return nil, err
	}
	return &info, nil
}
