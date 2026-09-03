package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/Radi-Labs/ARFL/internal/ecash"
)

// Compile-time check that Store implements ecash.Store.
var _ ecash.Store = (*Store)(nil)

// SaveKeyset persists a keyset record.
func (s *Store) SaveKeyset(ks *ecash.KeysetRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO mint_keysets (id, unit, active, derivation_path_idx, input_fee_ppk, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ks.ID, ks.Unit, ks.Active, ks.DerivationPathIdx, ks.InputFeePpk,
		ks.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	)
	return err
}

// GetActiveKeyset returns the currently active keyset record.
func (s *Store) GetActiveKeyset() (*ecash.KeysetRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, unit, active, derivation_path_idx, input_fee_ppk, created_at
		FROM mint_keysets WHERE active = 1 LIMIT 1`)
	return scanKeyset(row)
}

// GetKeyset returns a keyset by ID.
func (s *Store) GetKeyset(id string) (*ecash.KeysetRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, unit, active, derivation_path_idx, input_fee_ppk, created_at
		FROM mint_keysets WHERE id = ?`, id)
	return scanKeyset(row)
}

// GetAllKeysets returns all keyset records.
func (s *Store) GetAllKeysets() ([]*ecash.KeysetRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, unit, active, derivation_path_idx, input_fee_ppk, created_at
		FROM mint_keysets ORDER BY derivation_path_idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keysets []*ecash.KeysetRecord
	for rows.Next() {
		ks, err := scanKeysetRow(rows)
		if err != nil {
			return nil, err
		}
		keysets = append(keysets, ks)
	}
	return keysets, rows.Err()
}

// SaveMintQuote persists a new mint quote.
func (s *Store) SaveMintQuote(q *ecash.MintQuote) error {
	_, err := s.db.Exec(`
		INSERT INTO mint_quotes (id, amount, payment_request, payment_hash, state, expiry, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.Amount, q.PaymentRequest, q.PaymentHash, q.State, q.Expiry,
		q.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	)
	return err
}

// GetMintQuote retrieves a mint quote by ID.
func (s *Store) GetMintQuote(id string) (*ecash.MintQuote, error) {
	var q ecash.MintQuote
	var createdAt string
	err := s.db.QueryRow(`
		SELECT id, amount, payment_request, payment_hash, state, expiry, created_at
		FROM mint_quotes WHERE id = ?`, id).Scan(
		&q.ID, &q.Amount, &q.PaymentRequest, &q.PaymentHash, &q.State, &q.Expiry, &createdAt,
	)
	if err == sql.ErrNoRows {
		return nil, ecash.ErrQuoteNotFound
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// UpdateMintQuoteState transitions a mint quote to a new state.
// UpdateMintQuoteState sets a quote's state unconditionally.
//
// Prefer TransitionMintQuoteState for the paid-to-issued step: this cannot
// express "only if the quote has not already been issued", so using it there
// allows two concurrent requests to issue tokens against a single payment.
func (s *Store) UpdateMintQuoteState(id string, state ecash.QuoteState) error {
	res, err := s.db.Exec(`UPDATE mint_quotes SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ecash.ErrQuoteNotFound
	}
	return nil
}

// TransitionMintQuoteState moves a quote from one state to another only if it
// is currently in the expected state, reporting whether it won.
//
// The compare and the write are a single statement so the database decides the
// winner. This is what stops one paid invoice being redeemed for tokens twice
// when requests arrive concurrently, including against separate hub processes
// sharing the database, where an in-process lock would not help.
func (s *Store) TransitionMintQuoteState(id string, from, to ecash.QuoteState) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE mint_quotes SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SaveSpentProofs records proofs as spent (batch insert in a transaction).
//
// The y column is the primary key, so a proof already recorded by a concurrent
// request fails the insert. That is reported as ecash.ErrProofAlreadySpent so
// callers reject the spend rather than treating it as an internal fault. This
// is the backstop when several hub instances share one database and an
// in-process lock cannot serialise them.
func (s *Store) SaveSpentProofs(proofs []ecash.SpentProof) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO cashu_spent_proofs (y, keyset_id, amount, secret, c, spent_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range proofs {
		_, err := stmt.Exec(p.Y, p.KeysetID, p.Amount, p.Secret, p.C,
			p.SpentAt.UTC().Format("2006-01-02T15:04:05Z"))
		if err != nil {
			if isUniqueViolation(err) {
				return ecash.ErrProofAlreadySpent
			}
			return fmt.Errorf("inserting spent proof %s: %w", p.Y, err)
		}
	}

	return tx.Commit()
}

// isUniqueViolation reports whether err is a primary key or unique constraint
// failure.
//
// This matches on the message rather than the driver's typed error because
// go-sqlite3's error type only exists under cgo, and the non-cgo builds used
// for cross-compiling the client would not compile against it. Both messages
// below are the fixed strings SQLite emits for the two forms of the constraint.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE CONSTRAINT FAILED") ||
		strings.Contains(msg, "PRIMARY KEY MUST BE UNIQUE")
}

// IsProofSpent checks if a proof (identified by Y) has been spent.
func (s *Store) IsProofSpent(Y string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM cashu_spent_proofs WHERE y = ?`, Y).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetSpentProofsCount returns the total number of spent proofs.
func (s *Store) GetSpentProofsCount() (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM cashu_spent_proofs`).Scan(&count)
	return count, err
}

// scanKeyset scans a single keyset from a sql.Row.
func scanKeyset(row *sql.Row) (*ecash.KeysetRecord, error) {
	var ks ecash.KeysetRecord
	var createdAt string
	err := row.Scan(&ks.ID, &ks.Unit, &ks.Active, &ks.DerivationPathIdx, &ks.InputFeePpk, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ks, nil
}

// scanKeysetRow scans a keyset from sql.Rows.
func scanKeysetRow(rows *sql.Rows) (*ecash.KeysetRecord, error) {
	var ks ecash.KeysetRecord
	var createdAt string
	err := rows.Scan(&ks.ID, &ks.Unit, &ks.Active, &ks.DerivationPathIdx, &ks.InputFeePpk, &createdAt)
	if err != nil {
		return nil, err
	}
	return &ks, nil
}
