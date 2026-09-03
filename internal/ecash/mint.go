// Package ecash implements a Cashu-compatible mint for the ARFL hub.
// It uses Blind Diffie-Hellman Key Exchange (BDHKE) to issue and verify
// ecash tokens representing prepaid bandwidth quotas.
//
// The privacy guarantee: when a client redeems tokens for a VPN session,
// the hub cannot link the redemption to the original Lightning payment.
package ecash

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

// Errors returned by the mint.
var (
	ErrUnknownKeyset     = errors.New("unknown keyset")
	ErrInactiveKeyset    = errors.New("keyset is inactive")
	ErrInvalidProof      = errors.New("invalid proof")
	ErrProofAlreadySpent = errors.New("proof already spent")
	ErrDuplicateProofs   = errors.New("duplicate proofs in request")
	ErrAmountMismatch    = errors.New("input/output amount mismatch")
	ErrQuoteNotFound     = errors.New("mint quote not found")
	ErrQuoteNotPaid      = errors.New("mint quote not yet paid")
	ErrQuoteAlreadyUsed  = errors.New("mint quote already issued")
	ErrOutputOverQuote   = errors.New("output amount exceeds quote")
)

// QuoteState represents the lifecycle of a mint quote.
type QuoteState string

const (
	QuoteUnpaid  QuoteState = "UNPAID"
	QuotePaid    QuoteState = "PAID"
	QuoteIssued  QuoteState = "ISSUED"
	QuoteExpired QuoteState = "EXPIRED"
)

// MintQuote tracks a Lightning payment for minting ecash.
type MintQuote struct {
	ID             string     `json:"quote"`
	Amount         uint64     `json:"amount"`
	PaymentRequest string     `json:"request"`
	PaymentHash    string     `json:"payment_hash"`
	State          QuoteState `json:"state"`
	Expiry         int64      `json:"expiry"`
	CreatedAt      time.Time  `json:"created_at"`
}

// SpentProof records a redeemed proof to prevent double-spend.
type SpentProof struct {
	Y        string // hex-encoded point: SHA256(secret) mapped to curve
	KeysetID string
	Amount   uint64
	Secret   string
	C        string // hex-encoded unblinded signature
	SpentAt  time.Time
}

// Store defines the persistence interface for the mint.
type Store interface {
	// Keysets
	SaveKeyset(ks *KeysetRecord) error
	GetActiveKeyset() (*KeysetRecord, error)
	GetKeyset(id string) (*KeysetRecord, error)
	GetAllKeysets() ([]*KeysetRecord, error)

	// Mint quotes
	SaveMintQuote(q *MintQuote) error
	GetMintQuote(id string) (*MintQuote, error)
	UpdateMintQuoteState(id string, state QuoteState) error

	// Spent proofs
	//
	// SaveSpentProofs must be atomic across all proofs in the batch and must
	// return ErrProofAlreadySpent if any proof is already recorded, rather
	// than silently overwriting or partially inserting. Implementations
	// backed by a shared database are the only defence against double spends
	// when more than one mint process is running, since the mint's in-process
	// lock cannot serialise them.
	SaveSpentProofs(proofs []SpentProof) error
	IsProofSpent(Y string) (bool, error)
	GetSpentProofsCount() (int64, error)
}

// KeysetRecord is the serializable keyset metadata stored in the DB.
type KeysetRecord struct {
	ID                string
	Unit              string
	Active            bool
	DerivationPathIdx uint32
	InputFeePpk       uint
	CreatedAt         time.Time
}

// Mint is the ARFL hub's Cashu ecash mint.
type Mint struct {
	mu            sync.RWMutex
	activeKeyset  *gcrypto.MintKeyset
	keysets       map[string]*gcrypto.MintKeyset // keyset ID → keyset
	masterKey     *hdkeychain.ExtendedKey
	store         Store
	nextKeysetIdx uint32

	// spendMu serialises verify-then-mark so a proof cannot be accepted twice.
	// It is separate from mu, which guards keyset state and is held in read
	// mode during verification.
	spendMu sync.Mutex
}

// NewMint creates a mint from a master seed. If no keyset exists in the store,
// it generates the first one. Subsequent calls load existing keysets.
func NewMint(store Store, seed []byte) (*Mint, error) {
	masterKey, err := hdkeychain.NewMaster(seed, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("deriving master key: %w", err)
	}

	m := &Mint{
		keysets:   make(map[string]*gcrypto.MintKeyset),
		masterKey: masterKey,
		store:     store,
	}

	// Load existing keysets from store
	records, err := store.GetAllKeysets()
	if err != nil {
		return nil, fmt.Errorf("loading keysets: %w", err)
	}

	for _, rec := range records {
		ks, err := gcrypto.GenerateKeyset(masterKey, rec.DerivationPathIdx, rec.InputFeePpk)
		if err != nil {
			return nil, fmt.Errorf("regenerating keyset %s: %w", rec.ID, err)
		}
		ks.Active = rec.Active
		m.keysets[ks.Id] = ks
		if rec.Active {
			m.activeKeyset = ks
		}
		if rec.DerivationPathIdx >= m.nextKeysetIdx {
			m.nextKeysetIdx = rec.DerivationPathIdx + 1
		}
	}

	// If no active keyset, generate the first one
	if m.activeKeyset == nil {
		if err := m.generateKeyset(); err != nil {
			return nil, err
		}
	}

	return m, nil
}

// generateKeyset creates a new keyset and persists it.
func (m *Mint) generateKeyset() error {
	ks, err := gcrypto.GenerateKeyset(m.masterKey, m.nextKeysetIdx, 0)
	if err != nil {
		return fmt.Errorf("generating keyset: %w", err)
	}
	ks.Active = true

	rec := &KeysetRecord{
		ID:                ks.Id,
		Unit:              ks.Unit,
		Active:            true,
		DerivationPathIdx: m.nextKeysetIdx,
		InputFeePpk:       0,
		CreatedAt:         time.Now().UTC(),
	}
	if err := m.store.SaveKeyset(rec); err != nil {
		return fmt.Errorf("saving keyset: %w", err)
	}

	m.keysets[ks.Id] = ks
	m.activeKeyset = ks
	m.nextKeysetIdx++
	return nil
}

// ActiveKeysetID returns the ID of the currently active keyset.
func (m *Mint) ActiveKeysetID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeKeyset.Id
}

// PublicKeys returns the active keyset's public keys (amount → compressed pubkey hex).
func (m *Mint) PublicKeys() map[uint64]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.keysetPublicKeys(m.activeKeyset)
}

// KeysetPublicKeys returns public keys for a specific keyset.
func (m *Mint) KeysetPublicKeys(keysetID string) (map[uint64]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ks, ok := m.keysets[keysetID]
	if !ok {
		return nil, ErrUnknownKeyset
	}
	return m.keysetPublicKeys(ks), nil
}

func (m *Mint) keysetPublicKeys(ks *gcrypto.MintKeyset) map[uint64]string {
	pks := make(map[uint64]string, len(ks.Keys))
	for amount, kp := range ks.Keys {
		pks[amount] = hex.EncodeToString(kp.PublicKey.SerializeCompressed())
	}
	return pks
}

// KeysetInfos returns metadata about all keysets (for GET /v1/keysets).
func (m *Mint) KeysetInfos() []KeysetInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	infos := make([]KeysetInfo, 0, len(m.keysets))
	for _, ks := range m.keysets {
		infos = append(infos, KeysetInfo{
			ID:          ks.Id,
			Unit:        ks.Unit,
			Active:      ks.Active,
			InputFeePpk: ks.InputFeePpk,
		})
	}
	return infos
}

// KeysetInfo is the public metadata for a keyset.
type KeysetInfo struct {
	ID          string `json:"id"`
	Unit        string `json:"unit"`
	Active      bool   `json:"active"`
	InputFeePpk uint   `json:"input_fee_ppk"`
}

// SignBlindedMessages signs a set of blinded messages using the active keyset.
// Returns blinded signatures that the client can unblind.
func (m *Mint) SignBlindedMessages(outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sigs := make(cashu.BlindedSignatures, 0, len(outputs))
	for _, msg := range outputs {
		ks, ok := m.keysets[msg.Id]
		if !ok {
			return nil, ErrUnknownKeyset
		}
		if !ks.Active {
			return nil, ErrInactiveKeyset
		}

		kp, ok := ks.Keys[msg.Amount]
		if !ok {
			return nil, fmt.Errorf("no key for amount %d", msg.Amount)
		}

		B_, err := parsePublicKey(msg.B_)
		if err != nil {
			return nil, fmt.Errorf("invalid blinded message: %w", err)
		}

		C_ := gcrypto.SignBlindedMessage(B_, kp.PrivateKey)
		sig := cashu.BlindedSignature{
			Amount: msg.Amount,
			C_:     hex.EncodeToString(C_.SerializeCompressed()),
			Id:     ks.Id,
		}
		sigs = append(sigs, sig)
	}
	return sigs, nil
}

// VerifyProofs checks that each proof is valid and unspent.
// Returns the Y values (spent proof identifiers) for storage.
func (m *Mint) VerifyProofs(proofs cashu.Proofs) ([]SpentProof, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if cashu.CheckDuplicateProofs(proofs) {
		return nil, ErrDuplicateProofs
	}

	// Pre-allocate a reusable buffer for hex decoding compressed pubkeys (33 bytes).
	var cBuf [33]byte

	spent := make([]SpentProof, 0, len(proofs))
	for _, proof := range proofs {
		ks, ok := m.keysets[proof.Id]
		if !ok {
			return nil, ErrUnknownKeyset
		}

		kp, ok := ks.Keys[proof.Amount]
		if !ok {
			return nil, fmt.Errorf("no key for amount %d in keyset %s", proof.Amount, proof.Id)
		}

		// Decode C from hex into the reusable buffer (avoids allocation per proof).
		n, err := hex.Decode(cBuf[:], []byte(proof.C))
		if err != nil || n != 33 {
			return nil, fmt.Errorf("invalid C in proof: %w", err)
		}
		C, err := secp256k1.ParsePubKey(cBuf[:n])
		if err != nil {
			return nil, fmt.Errorf("invalid C in proof: %w", err)
		}

		// Compute Y = HashToCurve(secret) ONCE — used for both verification and spent tracking.
		// Previously we called gcrypto.Verify (which hashes internally) then HashToCurve again.
		Y, err := gcrypto.HashToCurve([]byte(proof.Secret))
		if err != nil {
			return nil, fmt.Errorf("hashing secret to curve: %w", err)
		}

		// Inline verification: k * Y == C (same as gcrypto.verify but avoids double HashToCurve).
		var yPoint, result secp256k1.JacobianPoint
		Y.AsJacobian(&yPoint)
		secp256k1.ScalarMultNonConst(&kp.PrivateKey.Key, &yPoint, &result)
		result.ToAffine()
		computed := secp256k1.NewPublicKey(&result.X, &result.Y)
		if !C.IsEqual(computed) {
			return nil, ErrInvalidProof
		}

		// Y is already computed — encode for spent tracking.
		yHex := hex.EncodeToString(Y.SerializeCompressed())

		// Check double-spend
		isSpent, err := m.store.IsProofSpent(yHex)
		if err != nil {
			return nil, fmt.Errorf("checking spent status: %w", err)
		}
		if isSpent {
			return nil, ErrProofAlreadySpent
		}

		spent = append(spent, SpentProof{
			Y:        yHex,
			KeysetID: proof.Id,
			Amount:   proof.Amount,
			Secret:   proof.Secret,
			C:        proof.C,
			SpentAt:  time.Now().UTC(),
		})
	}

	return spent, nil
}

// VerifyAndMarkSpent verifies proofs and records them as spent as one
// indivisible step.
//
// Verifying and marking separately is a double-spend hole: two concurrent
// requests carrying the same proof can both read it as unspent before either
// records it, and both are then accepted. spendMu closes that window, so every
// caller that intends to spend proofs must use this rather than calling
// VerifyProofs and SaveSpentProofs itself.
//
// The lock only covers one process. A hub running as several instances against
// a shared database still relies on the store rejecting a duplicate insert,
// which is why SaveSpentProofs reports an already-spent proof as
// ErrProofAlreadySpent rather than a generic failure.
func (m *Mint) VerifyAndMarkSpent(proofs cashu.Proofs) ([]SpentProof, error) {
	m.spendMu.Lock()
	defer m.spendMu.Unlock()

	spent, err := m.VerifyProofs(proofs)
	if err != nil {
		return nil, err
	}

	if err := m.store.SaveSpentProofs(spent); err != nil {
		return nil, fmt.Errorf("saving spent proofs: %w", err)
	}
	return spent, nil
}

// Swap verifies input proofs, marks them spent, and signs new outputs.
// The total input amount must equal total output amount (no fees for MVP).
func (m *Mint) Swap(inputs cashu.Proofs, outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	inputAmount := inputs.Amount()
	outputAmount := outputs.Amount()
	if inputAmount != outputAmount {
		return nil, ErrAmountMismatch
	}

	if _, err := m.VerifyAndMarkSpent(inputs); err != nil {
		return nil, err
	}

	// Sign new outputs
	sigs, err := m.SignBlindedMessages(outputs)
	if err != nil {
		return nil, err
	}

	return sigs, nil
}

// MintTokens issues blinded signatures for a paid quote.
func (m *Mint) MintTokens(quoteID string, outputs cashu.BlindedMessages) (cashu.BlindedSignatures, error) {
	quote, err := m.store.GetMintQuote(quoteID)
	if err != nil {
		return nil, ErrQuoteNotFound
	}

	switch quote.State {
	case QuoteUnpaid:
		return nil, ErrQuoteNotPaid
	case QuoteIssued:
		return nil, ErrQuoteAlreadyUsed
	case QuoteExpired:
		return nil, ErrQuoteNotPaid
	case QuotePaid:
		// proceed
	}

	// Verify output amount doesn't exceed quote
	outputAmount := outputs.Amount()
	if outputAmount > quote.Amount {
		return nil, ErrOutputOverQuote
	}

	// Sign the blinded messages
	sigs, err := m.SignBlindedMessages(outputs)
	if err != nil {
		return nil, err
	}

	// Mark quote as issued
	if err := m.store.UpdateMintQuoteState(quoteID, QuoteIssued); err != nil {
		return nil, fmt.Errorf("updating quote state: %w", err)
	}

	return sigs, nil
}

// CheckProofsState returns the state of each proof by Y value (spent or unspent).
func (m *Mint) CheckProofsState(Ys []string) ([]ProofState, error) {
	states := make([]ProofState, 0, len(Ys))
	for _, yHex := range Ys {
		spent, err := m.store.IsProofSpent(yHex)
		if err != nil {
			return nil, fmt.Errorf("checking state: %w", err)
		}

		state := ProofStateUnspent
		if spent {
			state = ProofStateSpent
		}
		states = append(states, ProofState{
			Y:     yHex,
			State: state,
		})
	}
	return states, nil
}

// ProofState represents the state of a Cashu proof.
type ProofState struct {
	Y     string         `json:"Y"`
	State ProofStateEnum `json:"state"`
}

// ProofStateEnum is the possible states of a proof.
type ProofStateEnum string

const (
	ProofStateUnspent ProofStateEnum = "UNSPENT"
	ProofStateSpent   ProofStateEnum = "SPENT"
	ProofStatePending ProofStateEnum = "PENDING"
)

// GenerateSeed creates a cryptographically random 32-byte seed.
func GenerateSeed() ([]byte, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generating random seed: %w", err)
	}
	return seed, nil
}

// parsePublicKey decodes a hex-encoded compressed public key.
func parsePublicKey(hexStr string) (*secp256k1.PublicKey, error) {
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, err
	}
	return secp256k1.ParsePubKey(b)
}

// HashSecret computes the Y point for a secret (used in spent proof tracking).
func HashSecret(secret string) (string, error) {
	Y, err := gcrypto.HashToCurve([]byte(secret))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(Y.SerializeCompressed()), nil
}

// GenerateQuoteID creates a random quote identifier.
func GenerateQuoteID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:16]), nil
}
