package payments

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Radi-Labs/ARFL/internal/credentials"
)

// --- Blind signature request/response types (Phase 4) ---

// RedeemRequest is the JSON body for POST /redeem.
// The client proves payment via preimage and requests blind signatures.
type RedeemRequest struct {
	PaymentPreimage string   `json:"payment_preimage"` // hex: 32 bytes = 64 hex chars
	KeyID           string   `json:"key_id"`           // denomination key to sign with
	BlindedMessages []string `json:"blinded_messages"` // hex-encoded blinded messages
	Nonce           string   `json:"nonce"`            // idempotency nonce (max 64 chars)
}

// RedeemResponse is the JSON response for POST /redeem.
type RedeemResponse struct {
	BlindSignatures []string `json:"blind_signatures"` // hex-encoded blind signatures
	BytesPerToken   int64    `json:"bytes_per_token"`
	TokensRedeemed  int      `json:"tokens_redeemed"`
	TokensRemaining int      `json:"tokens_remaining"`
}

// SpendRequest is the JSON body for POST /spend.
// A node submits a token for double-spend checking after local verification.
type SpendRequest struct {
	KeyID       string `json:"key_id"`
	TokenSecret string `json:"token_secret"` // hex: 32 bytes
	Signature   string `json:"signature"`    // hex: RSA unblinded signature
	NodePubkey  string `json:"node_pubkey"`  // reporting node's public key
}

// SpendResponse is the JSON response for POST /spend.
type SpendResponse struct {
	FirstSpend    bool  `json:"first_spend"`
	BytesPerToken int64 `json:"bytes_per_token"`
}

// EnableBlindSignatures registers the /redeem and /spend endpoints.
// The defaultKeyID specifies which denomination key to use for new entitlements.
// Call this after NewPurchaseAPI when Phase 4 blind signatures are enabled.
func (api *PurchaseAPI) EnableBlindSignatures(mint credentials.BlindMint, verifier credentials.BlindVerifier, defaultKeyID string) {
	api.blindMint = mint
	api.blindVerifier = verifier
	api.blindKeyID = defaultKeyID
	api.mux.HandleFunc("/redeem", api.handleRedeem)
	api.mux.HandleFunc("/spend", api.handleSpend)
}

// handleRedeem processes blind signature redemptions.
//
// Flow:
//  1. Validate preimage → derive payment_hash
//  2. Check idempotency (nonce + request_hash)
//  3. Look up entitlement by payment_hash
//  4. Atomically consume tokens BEFORE signing
//  5. Blind-sign messages
//  6. Cache redemption for crash recovery
//
// POST /redeem
func (api *PurchaseAPI) handleRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Body limit: blinded messages are ~256 bytes hex each, 500 max ≈ 128KB + overhead.
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)

	var req RedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// --- Validate preimage ---
	if len(req.PaymentPreimage) != 64 {
		jsonError(w, "payment_preimage must be 64 hex characters (32 bytes)", http.StatusBadRequest)
		return
	}
	preimage, err := hex.DecodeString(req.PaymentPreimage)
	if err != nil {
		jsonError(w, "payment_preimage must be valid hex", http.StatusBadRequest)
		return
	}

	// Derive payment_hash = SHA256(preimage). Only the payer knows the preimage.
	hashBytes := sha256.Sum256(preimage)
	paymentHash := hex.EncodeToString(hashBytes[:])

	// --- Validate nonce ---
	if req.Nonce == "" {
		jsonError(w, "nonce is required", http.StatusBadRequest)
		return
	}
	if len(req.Nonce) > 64 {
		jsonError(w, "nonce too long (max 64 chars)", http.StatusBadRequest)
		return
	}

	// --- Validate blinded messages ---
	if len(req.BlindedMessages) == 0 {
		jsonError(w, "blinded_messages must not be empty", http.StatusBadRequest)
		return
	}
	if len(req.BlindedMessages) > credentials.MaxBlindMessagesPerRequest {
		jsonError(w, fmt.Sprintf("too many messages: max %d", credentials.MaxBlindMessagesPerRequest), http.StatusBadRequest)
		return
	}

	// --- Validate key_id ---
	if req.KeyID == "" {
		jsonError(w, "key_id is required", http.StatusBadRequest)
		return
	}

	// --- Idempotency check ---
	requestHash := computeRequestHash(req.KeyID, req.BlindedMessages)

	existing, err := api.store.GetRedemption(req.Nonce)
	if err != nil {
		log.Printf("[blind-api] GetRedemption error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		if existing.RequestHash != requestHash {
			jsonError(w, "nonce conflict: already used with different request", http.StatusConflict)
			return
		}
		// Idempotent replay — return cached signatures.
		var sigs []string
		if err := json.Unmarshal([]byte(existing.BlindSignatures), &sigs); err != nil {
			log.Printf("[blind-api] unmarshal cached signatures: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		ent, err := api.store.GetEntitlementByPaymentHash(paymentHash)
		if err != nil {
			log.Printf("[blind-api] get entitlement for replay: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		denom, _ := api.blindMint.Denomination(req.KeyID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RedeemResponse{
			BlindSignatures: sigs,
			BytesPerToken:   denom,
			TokensRedeemed:  existing.TokensCount,
			TokensRemaining: ent.TokensRemaining,
		})
		return
	}

	// --- Look up entitlement ---
	ent, err := api.store.GetEntitlementByPaymentHash(paymentHash)
	if err != nil {
		jsonError(w, "no entitlement found for this payment", http.StatusNotFound)
		return
	}

	// Validate key_id matches the entitlement's denomination key.
	if ent.KeyID != req.KeyID {
		jsonError(w, fmt.Sprintf("key_id mismatch: entitlement uses %s", ent.KeyID), http.StatusBadRequest)
		return
	}

	// --- Decode blinded messages ---
	blindedMsgs := make([][]byte, len(req.BlindedMessages))
	for i, bm := range req.BlindedMessages {
		decoded, err := hex.DecodeString(bm)
		if err != nil {
			jsonError(w, fmt.Sprintf("blinded_messages[%d]: invalid hex", i), http.StatusBadRequest)
			return
		}
		blindedMsgs[i] = decoded
	}

	// --- Atomically consume tokens BEFORE signing ---
	// If this fails, no signatures are issued (no risk of free tokens).
	count := len(blindedMsgs)
	if err := api.store.ConsumeEntitlement(ent.ID, count); err != nil {
		jsonError(w, "insufficient tokens remaining", http.StatusPaymentRequired)
		return
	}

	// --- Blind-sign ---
	blindSigs, err := api.blindMint.SignBlinded(req.KeyID, blindedMsgs)
	if err != nil {
		// Tokens consumed but signing failed — critical bug, not client error.
		log.Printf("[blind-api] CRITICAL: tokens consumed but signing failed for %s: %v", paymentHash, err)
		jsonError(w, "signing failed (tokens consumed — contact support)", http.StatusInternalServerError)
		return
	}

	// Encode signatures as hex.
	sigStrings := make([]string, len(blindSigs))
	for i, sig := range blindSigs {
		sigStrings[i] = hex.EncodeToString(sig)
	}

	// --- Cache redemption for crash recovery ---
	sigsJSON, _ := json.Marshal(sigStrings)
	if err := api.store.InsertRedemption(req.Nonce, ent.ID, requestHash, count, string(sigsJSON)); err != nil {
		// Tokens consumed + sigs issued — cache failure is non-fatal but log loudly.
		log.Printf("[blind-api] WARNING: redemption cache failed for nonce %s: %v", req.Nonce, err)
	}

	// Fetch updated remaining count.
	updatedEnt, err := api.store.GetEntitlementByPaymentHash(paymentHash)
	remaining := 0
	if err == nil {
		remaining = updatedEnt.TokensRemaining
	}

	denom, _ := api.blindMint.Denomination(req.KeyID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RedeemResponse{
		BlindSignatures: sigStrings,
		BytesPerToken:   denom,
		TokensRedeemed:  count,
		TokensRemaining: remaining,
	})
}

// handleSpend validates a token and checks for double-spend.
//
// Flow:
//  1. Verify RSA blind signature on token
//  2. Derive token_id (domain-separated SHA256)
//  3. Atomically mark spent (INSERT ON CONFLICT DO NOTHING)
//  4. Return {first_spend, bytes_per_token}
//
// POST /spend
func (api *PurchaseAPI) handleSpend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096) // token envelope is small

	var req SpendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// --- Validate fields ---
	if req.KeyID == "" {
		jsonError(w, "key_id is required", http.StatusBadRequest)
		return
	}
	if req.TokenSecret == "" {
		jsonError(w, "token_secret is required", http.StatusBadRequest)
		return
	}
	if req.Signature == "" {
		jsonError(w, "signature is required", http.StatusBadRequest)
		return
	}
	if req.NodePubkey == "" {
		jsonError(w, "node_pubkey is required", http.StatusBadRequest)
		return
	}

	// --- Decode hex fields ---
	tokenSecret, err := hex.DecodeString(req.TokenSecret)
	if err != nil {
		jsonError(w, "token_secret must be valid hex", http.StatusBadRequest)
		return
	}
	sig, err := hex.DecodeString(req.Signature)
	if err != nil {
		jsonError(w, "signature must be valid hex", http.StatusBadRequest)
		return
	}

	// --- Reconstruct and verify token ---
	token := &credentials.BlindToken{
		Version:     credentials.BlindTokenVersion,
		KeyID:       req.KeyID,
		TokenSecret: tokenSecret,
		Signature:   sig,
	}

	if err := api.blindVerifier.Verify(token); err != nil {
		jsonError(w, "invalid token signature", http.StatusUnauthorized)
		return
	}

	// --- Atomic double-spend check ---
	tokenID := token.TokenID()
	firstSpend, err := api.store.MarkSpent(tokenID, req.KeyID, req.NodePubkey)
	if err != nil {
		log.Printf("[blind-api] MarkSpent error for token %s: %v", tokenID, err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	denom, _ := api.blindVerifier.Denomination(req.KeyID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SpendResponse{
		FirstSpend:    firstSpend,
		BytesPerToken: denom,
	})
}

// computeRequestHash creates a deterministic hash of the redemption request
// for idempotency checking. Binds key_id + all blinded messages.
func computeRequestHash(keyID string, blindedMessages []string) string {
	h := sha256.New()
	h.Write([]byte(keyID))
	h.Write([]byte("|"))
	h.Write([]byte(strings.Join(blindedMessages, "|")))
	return hex.EncodeToString(h.Sum(nil))
}
