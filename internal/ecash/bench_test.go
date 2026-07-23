package ecash

// Benchmarks for the ARFL Cashu mint's crypto hot path.
//
// WHY THESE BENCHMARKS MATTER:
//
// Every VPN connection triggers VerifyProofs() on the hub. If that
// function takes 5ms, the hub can only handle 200 redeems/sec per core.
// If we can push it to 0.5ms, that's 2000/sec — 10x more users on the
// same hardware.
//
// The three critical operations:
//   1. VerifyProofs   — runs on every /v1/redeem (hub, hottest path)
//   2. SignBlinded    — runs on every mint (hub, second hottest)
//   3. BlindMessage   — runs on every purchase (client-side)
//
// HOW TO READ BENCHMARK OUTPUT:
//
//   BenchmarkVerifyProofs_Single-8   5000   312400 ns/op   4520 B/op   62 allocs/op
//   ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ ^^^^   ^^^^^^^^^^^^^^ ^^^^^^^^^^^ ^^^^^^^^^^^^^
//   name + CPU count                 iters  nanoseconds    bytes       heap allocs
//                                           per operation  allocated   per operation
//
//   312400 ns/op = 0.31 ms/op = ~3200 ops/sec per core
//
// HOW TO RUN:
//
//   go test -bench=. -benchmem ./internal/ecash/
//   go test -bench=BenchmarkVerifyProofs -benchmem -count=5 ./internal/ecash/
//
// The -count=5 flag runs 5 times for statistical significance.
// Use `benchstat` to compare before/after optimizations:
//
//   go test -bench=. -benchmem -count=5 ./internal/ecash/ > before.txt
//   # ... make optimization ...
//   go test -bench=. -benchmem -count=5 ./internal/ecash/ > after.txt
//   benchstat before.txt after.txt

import (
	"encoding/hex"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

// neverSpentStore is a mock that always says "not spent."
// This isolates the CRYPTO cost from the STORE cost.
//
// Why? We want to know: "How fast is the elliptic curve math?"
// If we used the real store, we'd be benchmarking SQLite too,
// which muddies the signal. We benchmark storage separately.
type neverSpentStore struct {
	memStore
}

func (s *neverSpentStore) IsProofSpent(Y string) (bool, error) {
	return false, nil // always "not spent" — crypto runs every iteration
}

func (s *neverSpentStore) SaveSpentProofs(proofs []SpentProof) error {
	return nil // no-op — don't accumulate state
}

// newBenchMint creates a mint with the never-spent store.
// This means VerifyProofs will do full crypto but skip double-spend state.
func newBenchMint() *Mint {
	store := &neverSpentStore{memStore: *newMemStore()}
	m, err := NewMint(store, testSeed)
	if err != nil {
		panic("newBenchMint: " + err.Error())
	}
	return m
}

// generateProof creates a single valid proof for benchmarking.
// This is the full client-side flow: secret → blind → sign → unblind.
func generateProof(m *Mint, amount uint64) cashu.Proof {
	r, _ := secp256k1.GeneratePrivateKey()
	secret := hex.EncodeToString(r.Serialize()[:16])

	B_, _, _ := gcrypto.BlindMessage(secret, r)
	msg := cashu.NewBlindedMessage(m.ActiveKeysetID(), amount, B_)
	sigs, _ := m.SignBlindedMessages(cashu.BlindedMessages{msg})

	C_bytes, _ := hex.DecodeString(sigs[0].C_)
	C_, _ := secp256k1.ParsePubKey(C_bytes)

	pks := m.PublicKeys()
	kBytes, _ := hex.DecodeString(pks[amount])
	K, _ := secp256k1.ParsePubKey(kBytes)

	C := gcrypto.UnblindSignature(C_, r, K)

	return cashu.Proof{
		Amount: amount,
		Id:     m.ActiveKeysetID(),
		Secret: secret,
		C:      hex.EncodeToString(C.SerializeCompressed()),
	}
}

// =============================================================
// BENCHMARK 1: VerifyProofs — THE hottest path
// =============================================================
// This is what runs every time a node calls POST /v1/redeem.
// The hub must verify the proof's elliptic curve signature.
//
// What happens inside VerifyProofs:
//   1. HashToCurve(secret) → Y point on secp256k1
//   2. Check: k * Y == C  (point multiplication + comparison)
//   3. IsProofSpent(Y)     (our mock returns false instantly)
//
// The expensive part is step 2: scalar multiplication on secp256k1.

func BenchmarkVerifyProofs_Single(b *testing.B) {
	m := newBenchMint()
	proof := generateProof(m, 64)
	proofs := cashu.Proofs{proof}

	b.ReportAllocs()
	b.ResetTimer() // Don't count setup time

	for i := 0; i < b.N; i++ {
		_, _ = m.VerifyProofs(proofs)
	}
}

// Batch verification — how does cost scale with proof count?
// If 1 proof = 300µs, is 10 proofs = 3000µs (linear)?
// Or is there overhead that makes it worse?

func BenchmarkVerifyProofs_Batch4(b *testing.B) {
	m := newBenchMint()
	proofs := make(cashu.Proofs, 4)
	for i := range proofs {
		proofs[i] = generateProof(m, 8)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.VerifyProofs(proofs)
	}
}

func BenchmarkVerifyProofs_Batch16(b *testing.B) {
	m := newBenchMint()
	proofs := make(cashu.Proofs, 16)
	for i := range proofs {
		proofs[i] = generateProof(m, 4)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.VerifyProofs(proofs)
	}
}

// =============================================================
// BENCHMARK 2: SignBlindedMessages — hub-side minting
// =============================================================
// Runs when a client mints tokens after paying Lightning.
// Less frequent than VerifyProofs, but still CPU-bound.
//
// What happens: for each output, the mint does:
//   k * B_  (scalar multiplication of blinded point)

func BenchmarkSignBlinded_Single(b *testing.B) {
	m := newBenchMint()

	r, _ := secp256k1.GeneratePrivateKey()
	secret := hex.EncodeToString(r.Serialize()[:16])
	B_, _, _ := gcrypto.BlindMessage(secret, r)
	msg := cashu.NewBlindedMessage(m.ActiveKeysetID(), 64, B_)
	msgs := cashu.BlindedMessages{msg}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.SignBlindedMessages(msgs)
	}
}

func BenchmarkSignBlinded_Batch8(b *testing.B) {
	m := newBenchMint()

	msgs := make(cashu.BlindedMessages, 8)
	for i := range msgs {
		r, _ := secp256k1.GeneratePrivateKey()
		secret := hex.EncodeToString(r.Serialize()[:16])
		B_, _, _ := gcrypto.BlindMessage(secret, r)
		msgs[i] = cashu.NewBlindedMessage(m.ActiveKeysetID(), 8, B_)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = m.SignBlindedMessages(msgs)
	}
}

// =============================================================
// BENCHMARK 3: Client-side operations
// =============================================================
// These run on the user's device. Less critical for hub capacity,
// but important for mobile (slow CPU) and UX (user waits).

func BenchmarkBlindMessage(b *testing.B) {
	r, _ := secp256k1.GeneratePrivateKey()
	secret := hex.EncodeToString(r.Serialize()[:16])

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _, _ = gcrypto.BlindMessage(secret, r)
	}
}

func BenchmarkHashToCurve(b *testing.B) {
	// HashToCurve is the first step of both BlindMessage and VerifyProofs.
	// It's iterative: hash, check if valid x-coord, increment counter.
	// Average ~2 iterations, but worst-case is unbounded (probabilistic).
	secret := []byte("benchmark-secret-value-for-hash-to-curve")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = gcrypto.HashToCurve(secret)
	}
}

func BenchmarkUnblindSignature(b *testing.B) {
	m := newBenchMint()
	r, _ := secp256k1.GeneratePrivateKey()
	secret := hex.EncodeToString(r.Serialize()[:16])

	B_, _, _ := gcrypto.BlindMessage(secret, r)
	msg := cashu.NewBlindedMessage(m.ActiveKeysetID(), 64, B_)
	sigs, _ := m.SignBlindedMessages(cashu.BlindedMessages{msg})

	C_bytes, _ := hex.DecodeString(sigs[0].C_)
	C_, _ := secp256k1.ParsePubKey(C_bytes)

	pks := m.PublicKeys()
	kBytes, _ := hex.DecodeString(pks[64])
	K, _ := secp256k1.ParsePubKey(kBytes)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = gcrypto.UnblindSignature(C_, r, K)
	}
}

