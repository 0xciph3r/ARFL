package ecash

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

// memStore is an in-memory implementation of Store for testing.
type memStore struct {
	keysets     map[string]*KeysetRecord
	quotes      map[string]*MintQuote
	spentProofs map[string]bool

	// quoteMu guards the compare-and-set in TransitionMintQuoteState, which
	// concurrency tests rely on to pick a single winner.
	quoteMu sync.Mutex
}

func newMemStore() *memStore {
	return &memStore{
		keysets:     make(map[string]*KeysetRecord),
		quotes:      make(map[string]*MintQuote),
		spentProofs: make(map[string]bool),
	}
}

func (s *memStore) SaveKeyset(ks *KeysetRecord) error {
	s.keysets[ks.ID] = ks
	return nil
}

func (s *memStore) GetActiveKeyset() (*KeysetRecord, error) {
	for _, ks := range s.keysets {
		if ks.Active {
			return ks, nil
		}
	}
	return nil, nil
}

func (s *memStore) GetKeyset(id string) (*KeysetRecord, error) {
	ks, ok := s.keysets[id]
	if !ok {
		return nil, nil
	}
	return ks, nil
}

func (s *memStore) GetAllKeysets() ([]*KeysetRecord, error) {
	var all []*KeysetRecord
	for _, ks := range s.keysets {
		all = append(all, ks)
	}
	return all, nil
}

func (s *memStore) SaveMintQuote(q *MintQuote) error {
	s.quotes[q.ID] = q
	return nil
}

func (s *memStore) GetMintQuote(id string) (*MintQuote, error) {
	q, ok := s.quotes[id]
	if !ok {
		return nil, ErrQuoteNotFound
	}
	return q, nil
}

func (s *memStore) UpdateMintQuoteState(id string, state QuoteState) error {
	q, ok := s.quotes[id]
	if !ok {
		return ErrQuoteNotFound
	}
	q.State = state
	return nil
}

// TransitionMintQuoteState compares and sets under the store's own lock, so
// only one caller can move a quote out of a given state.
func (s *memStore) TransitionMintQuoteState(id string, from, to QuoteState) (bool, error) {
	s.quoteMu.Lock()
	defer s.quoteMu.Unlock()

	q, ok := s.quotes[id]
	if !ok {
		return false, ErrQuoteNotFound
	}
	if q.State != from {
		return false, nil
	}
	q.State = to
	return true, nil
}

func (s *memStore) SaveSpentProofs(proofs []SpentProof) error {
	for _, p := range proofs {
		if s.spentProofs[p.Y] {
			return ErrProofAlreadySpent
		}
		s.spentProofs[p.Y] = true
	}
	return nil
}

func (s *memStore) IsProofSpent(Y string) (bool, error) {
	return s.spentProofs[Y], nil
}

func (s *memStore) GetSpentProofsCount() (int64, error) {
	return int64(len(s.spentProofs)), nil
}

// testSeed is a fixed seed for deterministic tests.
var testSeed = []byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

func newTestMint(t *testing.T) *Mint {
	t.Helper()
	store := newMemStore()
	m, err := NewMint(store, testSeed)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}
	return m
}

func TestNewMint_GeneratesKeyset(t *testing.T) {
	m := newTestMint(t)

	if m.ActiveKeysetID() == "" {
		t.Fatal("expected active keyset ID")
	}

	pks := m.PublicKeys()
	if len(pks) == 0 {
		t.Fatal("expected public keys")
	}

	// Should have keys for power-of-2 denominations
	for _, amt := range []uint64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024} {
		if _, ok := pks[amt]; !ok {
			t.Errorf("missing public key for denomination %d", amt)
		}
	}
}

func TestNewMint_ReloadsKeyset(t *testing.T) {
	store := newMemStore()

	m1, err := NewMint(store, testSeed)
	if err != nil {
		t.Fatalf("NewMint: %v", err)
	}
	keysetID := m1.ActiveKeysetID()

	// Create second mint from same store — should load, not generate.
	m2, err := NewMint(store, testSeed)
	if err != nil {
		t.Fatalf("NewMint reload: %v", err)
	}

	if m2.ActiveKeysetID() != keysetID {
		t.Errorf("expected same keyset ID %s, got %s", keysetID, m2.ActiveKeysetID())
	}
}

func TestKeysetInfos(t *testing.T) {
	m := newTestMint(t)
	infos := m.KeysetInfos()

	if len(infos) != 1 {
		t.Fatalf("expected 1 keyset info, got %d", len(infos))
	}
	if !infos[0].Active {
		t.Error("keyset should be active")
	}
	if infos[0].Unit != "sat" {
		t.Errorf("expected unit 'sat', got %q", infos[0].Unit)
	}
}

// blindAndSign performs a full client-side blind → mint sign → client unblind cycle.
func blindAndSign(t *testing.T, m *Mint, amount uint64) cashu.Proof {
	t.Helper()

	// Client generates a random secret and blinding factor.
	r, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate r: %v", err)
	}
	secret := hex.EncodeToString(r.Serialize()[:16])

	// Client blinds the secret: B_ = Y + rG
	B_, _, err := gcrypto.BlindMessage(secret, r)
	if err != nil {
		t.Fatalf("blind message: %v", err)
	}

	// Create blinded message.
	msg := cashu.NewBlindedMessage(m.ActiveKeysetID(), amount, B_)

	// Mint signs.
	sigs, err := m.SignBlindedMessages(cashu.BlindedMessages{msg})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}

	// Client unblinds: C = C_ - rK
	C_, err := parsePublicKey(sigs[0].C_)
	if err != nil {
		t.Fatalf("parse C_: %v", err)
	}

	pks := m.PublicKeys()
	K, err := parsePublicKey(pks[amount])
	if err != nil {
		t.Fatalf("parse K: %v", err)
	}

	C := gcrypto.UnblindSignature(C_, r, K)

	return cashu.Proof{
		Amount: amount,
		Id:     m.ActiveKeysetID(),
		Secret: secret,
		C:      hex.EncodeToString(C.SerializeCompressed()),
	}
}

func TestBlindSignAndVerify(t *testing.T) {
	m := newTestMint(t)

	proof := blindAndSign(t, m, 8)

	// Verify the proof.
	spent, err := m.VerifyProofs(cashu.Proofs{proof})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(spent) != 1 {
		t.Fatalf("expected 1 spent proof, got %d", len(spent))
	}
	if spent[0].Amount != 8 {
		t.Errorf("expected amount 8, got %d", spent[0].Amount)
	}
}

func TestDoubleSpendPrevention(t *testing.T) {
	m := newTestMint(t)

	proof := blindAndSign(t, m, 4)

	// First redemption: verify and mark spent.
	spent, err := m.VerifyProofs(cashu.Proofs{proof})
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := m.store.SaveSpentProofs(spent); err != nil {
		t.Fatalf("save spent: %v", err)
	}

	// Second redemption: should fail.
	_, err = m.VerifyProofs(cashu.Proofs{proof})
	if err != ErrProofAlreadySpent {
		t.Fatalf("expected ErrProofAlreadySpent, got: %v", err)
	}
}

func TestSwap(t *testing.T) {
	m := newTestMint(t)

	// Create a 16-sat proof.
	proof16 := blindAndSign(t, m, 16)

	// Split into 8 + 4 + 4 = 16.
	r1, _ := secp256k1.GeneratePrivateKey()
	r2, _ := secp256k1.GeneratePrivateKey()
	r3, _ := secp256k1.GeneratePrivateKey()

	s1 := hex.EncodeToString(r1.Serialize()[:16])
	s2 := hex.EncodeToString(r2.Serialize()[:16])
	s3 := hex.EncodeToString(r3.Serialize()[:16])

	B1, _, _ := gcrypto.BlindMessage(s1, r1)
	B2, _, _ := gcrypto.BlindMessage(s2, r2)
	B3, _, _ := gcrypto.BlindMessage(s3, r3)

	outputs := cashu.BlindedMessages{
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 8, B1),
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 4, B2),
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 4, B3),
	}

	sigs, err := m.Swap(cashu.Proofs{proof16}, outputs)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if len(sigs) != 3 {
		t.Fatalf("expected 3 signatures, got %d", len(sigs))
	}

	// Original proof should now be spent.
	_, err = m.VerifyProofs(cashu.Proofs{proof16})
	if err != ErrProofAlreadySpent {
		t.Fatalf("expected original proof to be spent, got: %v", err)
	}
}

func TestSwap_AmountMismatch(t *testing.T) {
	m := newTestMint(t)

	proof := blindAndSign(t, m, 8)

	// Try to swap 8 sats for 16 sats output (should fail).
	r, _ := secp256k1.GeneratePrivateKey()
	s := hex.EncodeToString(r.Serialize()[:16])
	B_, _, _ := gcrypto.BlindMessage(s, r)

	outputs := cashu.BlindedMessages{
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 16, B_),
	}

	_, err := m.Swap(cashu.Proofs{proof}, outputs)
	if err != ErrAmountMismatch {
		t.Fatalf("expected ErrAmountMismatch, got: %v", err)
	}
}

func TestMintTokens_QuoteFlow(t *testing.T) {
	m := newTestMint(t)

	// Create a mint quote (simulating paid Lightning invoice).
	quote := &MintQuote{
		ID:             "test-quote-1",
		Amount:         100,
		PaymentRequest: "lnbc100n1...",
		PaymentHash:    "abc123",
		State:          QuotePaid,
		Expiry:         time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().UTC(),
	}
	if err := m.store.SaveMintQuote(quote); err != nil {
		t.Fatalf("save quote: %v", err)
	}

	// Mint tokens: 64 + 32 + 4 = 100 sats.
	r1, _ := secp256k1.GeneratePrivateKey()
	r2, _ := secp256k1.GeneratePrivateKey()
	r3, _ := secp256k1.GeneratePrivateKey()

	s1 := hex.EncodeToString(r1.Serialize()[:16])
	s2 := hex.EncodeToString(r2.Serialize()[:16])
	s3 := hex.EncodeToString(r3.Serialize()[:16])

	B1, _, _ := gcrypto.BlindMessage(s1, r1)
	B2, _, _ := gcrypto.BlindMessage(s2, r2)
	B3, _, _ := gcrypto.BlindMessage(s3, r3)

	outputs := cashu.BlindedMessages{
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 64, B1),
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 32, B2),
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 4, B3),
	}

	sigs, err := m.MintTokens("test-quote-1", outputs)
	if err != nil {
		t.Fatalf("mint tokens: %v", err)
	}
	if len(sigs) != 3 {
		t.Fatalf("expected 3 signatures, got %d", len(sigs))
	}

	// Quote should now be ISSUED — can't mint again.
	_, err = m.MintTokens("test-quote-1", outputs)
	if err != ErrQuoteAlreadyUsed {
		t.Fatalf("expected ErrQuoteAlreadyUsed, got: %v", err)
	}
}

func TestMintTokens_UnpaidQuote(t *testing.T) {
	m := newTestMint(t)

	quote := &MintQuote{
		ID:             "unpaid-quote",
		Amount:         50,
		PaymentRequest: "lnbc50n1...",
		PaymentHash:    "def456",
		State:          QuoteUnpaid,
		Expiry:         time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().UTC(),
	}
	_ = m.store.SaveMintQuote(quote)

	r, _ := secp256k1.GeneratePrivateKey()
	s := hex.EncodeToString(r.Serialize()[:16])
	B_, _, _ := gcrypto.BlindMessage(s, r)
	outputs := cashu.BlindedMessages{
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 50, B_),
	}

	_, err := m.MintTokens("unpaid-quote", outputs)
	if err != ErrQuoteNotPaid {
		t.Fatalf("expected ErrQuoteNotPaid, got: %v", err)
	}
}

func TestMintTokens_OverQuote(t *testing.T) {
	m := newTestMint(t)

	quote := &MintQuote{
		ID:     "small-quote",
		Amount: 10,
		State:  QuotePaid,
		Expiry: time.Now().Add(time.Hour).Unix(),
	}
	_ = m.store.SaveMintQuote(quote)

	r, _ := secp256k1.GeneratePrivateKey()
	s := hex.EncodeToString(r.Serialize()[:16])
	B_, _, _ := gcrypto.BlindMessage(s, r)
	outputs := cashu.BlindedMessages{
		cashu.NewBlindedMessage(m.ActiveKeysetID(), 16, B_),
	}

	_, err := m.MintTokens("small-quote", outputs)
	if err != ErrOutputOverQuote {
		t.Fatalf("expected ErrOutputOverQuote, got: %v", err)
	}
}

func TestCheckProofsState(t *testing.T) {
	m := newTestMint(t)

	proof := blindAndSign(t, m, 8)

	// Compute Y for the proof's secret.
	Y, _ := gcrypto.HashToCurve([]byte(proof.Secret))
	yHex := hex.EncodeToString(Y.SerializeCompressed())

	// Before spending: should be UNSPENT.
	states, err := m.CheckProofsState([]string{yHex})
	if err != nil {
		t.Fatalf("check state: %v", err)
	}
	if states[0].State != ProofStateUnspent {
		t.Errorf("expected UNSPENT, got %s", states[0].State)
	}

	// Spend it.
	spent, _ := m.VerifyProofs(cashu.Proofs{proof})
	_ = m.store.SaveSpentProofs(spent)

	// After spending: should be SPENT.
	states, err = m.CheckProofsState([]string{yHex})
	if err != nil {
		t.Fatalf("check state: %v", err)
	}
	if states[0].State != ProofStateSpent {
		t.Errorf("expected SPENT, got %s", states[0].State)
	}
}

func TestGenerateSeed(t *testing.T) {
	seed, err := GenerateSeed()
	if err != nil {
		t.Fatalf("generate seed: %v", err)
	}
	if len(seed) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(seed))
	}

	// Should be unique.
	seed2, _ := GenerateSeed()
	if hex.EncodeToString(seed) == hex.EncodeToString(seed2) {
		t.Fatal("two seeds should not be identical")
	}
}

func TestDuplicateProofs(t *testing.T) {
	m := newTestMint(t)

	proof := blindAndSign(t, m, 4)

	// Send same proof twice in one request.
	_, err := m.VerifyProofs(cashu.Proofs{proof, proof})
	if err != ErrDuplicateProofs {
		t.Fatalf("expected ErrDuplicateProofs, got: %v", err)
	}
}
