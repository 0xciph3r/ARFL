package nostr

// Benchmarks for the NIP-44 encryption hot path.
//
// This code runs on every VPN connection:
//   - Client: Encrypt (seal token envelope for each node)
//   - Node:   Decrypt (open envelope to extract Cashu proofs)
//   - Both:   GetConversationKey (ECDH, once per session)
//
// The node's Decrypt is the binding constraint — it runs on every
// incoming connection event on the 1-vCPU node server.
//
// HOW TO RUN:
//
//   go test -bench=. -benchmem ./internal/nostr/
//   go test -bench=BenchmarkNIP44 -benchmem -count=5 ./internal/nostr/

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/elnosh/gonuts/cashu"
)

// =============================================================
// BENCHMARK: GetConversationKey (ECDH)
// =============================================================
// This is the secp256k1 scalar multiplication: privA × PubB.
// It runs ONCE per session (the result is cached/reused for
// all messages between the same two parties).
//
// Cost breakdown:
//   - Convert pubkey to Jacobian coordinates
//   - ScalarMultNonConst (the expensive part: ~250 EC doublings/additions)
//   - Convert back to affine (1 modular inversion)
//   - HKDF-extract (SHA-256 based, cheap)

func BenchmarkNIP44_GetConversationKey(b *testing.B) {
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	pubB := privB.PubKey()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = GetConversationKey(privA, pubB)
	}
}

// =============================================================
// BENCHMARK: Encrypt — client-side cost per node
// =============================================================
// The client encrypts a TokenPayload for each node (entry + exit).
// Payload size varies: ~200 bytes for 1 proof, ~2KB for 10 proofs.
//
// Cost breakdown:
//   - Random nonce (32 bytes from crypto/rand)
//   - HKDF-expand (derive per-message keys: 76 bytes)
//   - Pad plaintext (power-of-2 padding)
//   - ChaCha20 XOR (very fast, ~1 cycle/byte on modern CPUs)
//   - HMAC-SHA256 over nonce+ciphertext
//   - Base64 encode

func BenchmarkNIP44_Encrypt_SmallPayload(b *testing.B) {
	// Typical: 1 Cashu proof + WG pubkey (~200 bytes JSON)
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	convKey, _ := GetConversationKey(privA, privB.PubKey())

	payload := fakeTokenPayloadJSON(1) // ~200 bytes

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Encrypt(payload, convKey)
	}
}

func BenchmarkNIP44_Encrypt_MediumPayload(b *testing.B) {
	// 4 proofs (~800 bytes JSON) — typical 2-hop split
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	convKey, _ := GetConversationKey(privA, privB.PubKey())

	payload := fakeTokenPayloadJSON(4)

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Encrypt(payload, convKey)
	}
}

func BenchmarkNIP44_Encrypt_LargePayload(b *testing.B) {
	// 16 proofs (~3KB JSON) — large batch
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	convKey, _ := GetConversationKey(privA, privB.PubKey())

	payload := fakeTokenPayloadJSON(16)

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Encrypt(payload, convKey)
	}
}

// =============================================================
// BENCHMARK: Decrypt — node-side cost per connection
// =============================================================
// This is what the node runs for every incoming token event.
// It must be fast because the node processes events sequentially
// (from the Nostr relay subscription channel).

func BenchmarkNIP44_Decrypt_SmallPayload(b *testing.B) {
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	convKey, _ := GetConversationKey(privA, privB.PubKey())

	payload := fakeTokenPayloadJSON(1)
	encrypted, _ := Encrypt(payload, convKey)

	// Node uses its own privkey + sender's pubkey to derive same convKey
	nodeConvKey, _ := GetConversationKey(privB, privA.PubKey())

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Decrypt(encrypted, nodeConvKey)
	}
}

func BenchmarkNIP44_Decrypt_MediumPayload(b *testing.B) {
	privA, _ := btcec.NewPrivateKey()
	privB, _ := btcec.NewPrivateKey()
	convKey, _ := GetConversationKey(privA, privB.PubKey())

	payload := fakeTokenPayloadJSON(4)
	encrypted, _ := Encrypt(payload, convKey)

	nodeConvKey, _ := GetConversationKey(privB, privA.PubKey())

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = Decrypt(encrypted, nodeConvKey)
	}
}

// =============================================================
// BENCHMARK: Full SealTokenEnvelope — complete client operation
// =============================================================
// This is the all-in cost of preparing one encrypted event:
//   ECDH + Encrypt + Build Nostr event + Sign (Schnorr)
//
// In production, the client does this TWICE (entry + exit node).

func BenchmarkNIP44_SealTokenEnvelope(b *testing.B) {
	senderKP, _ := GenerateKeyPair()
	recipientKP, _ := GenerateKeyPair()
	recipientHex := recipientKP.PubkeyHex()

	payload := &TokenPayload{
		Proofs:   fakeCashuProofs(2),
		WGPubkey: "clientWGPubkeyBase64==",
		Role:     "entry",
		Version:  1,
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = SealTokenEnvelope(senderKP, recipientHex, payload)
	}
}

// =============================================================
// BENCHMARK: Full OpenTokenEnvelope — complete node operation
// =============================================================
// Node receives event from relay → decrypts → extracts payload.
// This is: Parse pubkey + ECDH + Decrypt + JSON unmarshal.

func BenchmarkNIP44_OpenTokenEnvelope(b *testing.B) {
	senderKP, _ := GenerateKeyPair()
	recipientKP, _ := GenerateKeyPair()

	payload := &TokenPayload{
		Proofs:   fakeCashuProofs(2),
		WGPubkey: "clientWGPubkeyBase64==",
		Role:     "entry",
		Version:  1,
	}
	event, _ := SealTokenEnvelope(senderKP, recipientKP.PubkeyHex(), payload)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = OpenTokenEnvelope(event, recipientKP)
	}
}

// =============================================================
// Helpers
// =============================================================

// fakeTokenPayloadJSON generates a realistic TokenPayload JSON string.
func fakeTokenPayloadJSON(proofCount int) string {
	payload := TokenPayload{
		Proofs:   fakeCashuProofs(proofCount),
		WGPubkey: "dGVzdC13aXJlZ3VhcmQtcHVia2V5LWJhc2U2NA==",
		Role:     "entry",
		Version:  1,
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// fakeCashuProofs generates realistic-sized Cashu proofs.
func fakeCashuProofs(count int) cashu.Proofs {
	proofs := make(cashu.Proofs, count)
	for i := range proofs {
		proofs[i] = cashu.Proof{
			Amount: 64,
			Id:     "00eb7476a759a27e",
			Secret: strings.Repeat("a", 64),        // 64-char hex secret
			C:      "02" + strings.Repeat("b", 64), // compressed pubkey
		}
	}
	return proofs
}
