package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/ecash"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/elnosh/gonuts/cashu"
)

const (
	// maxCashuBodyBytes caps request body size for all ecash endpoints.
	maxCashuBodyBytes = 64 * 1024 // 64 KB
	// maxCheckStateYs caps the number of Y values in a checkstate request.
	maxCheckStateYs = 100
	// maxSwapInputs caps input proofs per swap.
	maxSwapInputs = 64
	// maxSwapOutputs caps outputs per swap to prevent resource exhaustion.
	maxSwapOutputs = 64
	// maxMintOutputs caps outputs per mint request.
	maxMintOutputs = 64
	// maxQuoteAmount caps the maximum sats per mint quote.
	maxQuoteAmount = 1_000_000 // 1M sats
	// cryptoTimeout is the max time a request will wait for a worker slot.
	cryptoTimeout = 10 * time.Second
)

// SetMint wires the Cashu mint, worker pool, and registers ecash API endpoints.
func (api *DiscoveryAPI) SetMint(mint *ecash.Mint, mintStore ecash.Store) {
	api.mint = mint
	api.mintStore = mintStore
	api.cryptoPool = ecash.NewWorkerPool(mint, 0) // 0 = use GOMAXPROCS

	// NUT-01: Mint public keys
	api.mux.HandleFunc("/v1/keys", api.handleKeys)
	// NUT-02: Keysets
	api.mux.HandleFunc("/v1/keysets", api.handleKeysets)
	// NUT-04: Mint quote (request + check + mint)
	api.mux.HandleFunc("/v1/mint/quote/bolt11", api.handleMintQuote)
	api.mux.HandleFunc("/v1/mint/bolt11", api.handleMintTokens)
	// NUT-03: Swap
	api.mux.HandleFunc("/v1/swap", api.handleSwap)
	// NUT-07: Token state check
	api.mux.HandleFunc("/v1/checkstate", api.handleCheckState)
}

// cashuRateCheck applies rate limiting and returns the client IP.
// Returns "" and writes a 429 if the client is rate-limited.
func (api *DiscoveryAPI) cashuRateCheck(w http.ResponseWriter, r *http.Request) string {
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	if !api.checkRateLimit(clientIP) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return ""
	}
	return clientIP
}

// limitedBody returns a size-capped reader for the request body.
func limitedBody(r *http.Request) io.Reader {
	return io.LimitReader(r.Body, maxCashuBodyBytes)
}

// isCircuitOpen checks if a Lightning error is a circuit breaker rejection
// and writes the appropriate 503 response.
func isCircuitOpen(w http.ResponseWriter, err error) bool {
	if errors.Is(err, lightning.ErrCircuitOpen) {
		writeError(w, http.StatusServiceUnavailable,
			"lightning temporarily unavailable — try again shortly")
		return true
	}
	return false
}

// handleKeys returns the active keyset's public keys (NUT-01).
func (api *DiscoveryAPI) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	pks := api.mint.PublicKeys()
	resp := keysResponse{
		Keysets: []keysetKeys{{
			ID:          api.mint.ActiveKeysetID(),
			Unit:        "sat",
			Keys:        pks,
			InputFeePpk: 0,
		}},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type keysResponse struct {
	Keysets []keysetKeys `json:"keysets"`
}

type keysetKeys struct {
	ID          string            `json:"id"`
	Unit        string            `json:"unit"`
	Keys        map[uint64]string `json:"keys"`
	InputFeePpk uint              `json:"input_fee_ppk"`
}

// handleKeysets returns metadata about all keysets (NUT-02).
func (api *DiscoveryAPI) handleKeysets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	infos := api.mint.KeysetInfos()
	resp := keysetsResponse{Keysets: infos}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type keysetsResponse struct {
	Keysets []ecash.KeysetInfo `json:"keysets"`
}

// handleMintQuote handles both creating and checking mint quotes (NUT-04).
// POST /v1/mint/quote/bolt11 — create a new quote
// GET  /v1/mint/quote/bolt11/{quote_id} — check quote status
func (api *DiscoveryAPI) handleMintQuote(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		api.handleCreateMintQuote(w, r)
	case http.MethodGet:
		api.handleGetMintQuote(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCreateMintQuote creates a Lightning invoice for minting ecash.
func (api *DiscoveryAPI) handleCreateMintQuote(w http.ResponseWriter, r *http.Request) {
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	var req struct {
		Amount uint64 `json:"amount"`
		Unit   string `json:"unit"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Amount == 0 {
		writeError(w, http.StatusBadRequest, "amount must be > 0")
		return
	}
	if req.Amount > maxQuoteAmount {
		writeError(w, http.StatusBadRequest, "amount exceeds maximum (1M sats)")
		return
	}
	if req.Unit != "" && req.Unit != "sat" {
		writeError(w, http.StatusBadRequest, "only 'sat' unit supported")
		return
	}

	if api.lnc == nil {
		writeError(w, http.StatusServiceUnavailable, "lightning not configured")
		return
	}

	// Create Lightning invoice (circuit breaker will fail fast if LND is down)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	invoice, err := api.lnc.CreateInvoice(ctx, int64(req.Amount),
		"ARFL ecash mint", 15*time.Minute)
	if err != nil {
		if isCircuitOpen(w, err) {
			return
		}
		log.Printf("[ecash] create invoice error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create invoice")
		return
	}

	// Generate quote ID
	quoteID, err := ecash.GenerateQuoteID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate quote ID")
		return
	}

	quote := &ecash.MintQuote{
		ID:             quoteID,
		Amount:         req.Amount,
		PaymentRequest: invoice.PaymentRequest,
		PaymentHash:    invoice.PaymentHash,
		State:          ecash.QuoteUnpaid,
		Expiry:         invoice.ExpiresAt.Unix(),
		CreatedAt:      time.Now().UTC(),
	}

	if err := api.mintStore.SaveMintQuote(quote); err != nil {
		log.Printf("[ecash] save quote error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to save quote")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(quote)
}

// handleGetMintQuote checks the status of a mint quote.
// The quote ID is the last path segment: /v1/mint/quote/bolt11/{id}
func (api *DiscoveryAPI) handleGetMintQuote(w http.ResponseWriter, r *http.Request) {
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	// Extract quote ID from path: /v1/mint/quote/bolt11/{id}
	path := strings.TrimPrefix(r.URL.Path, "/v1/mint/quote/bolt11/")
	if path == "" || path == r.URL.Path {
		writeError(w, http.StatusBadRequest, "quote ID required")
		return
	}
	quoteID := path

	// Reject obviously invalid quote IDs (must be hex, max 64 chars).
	if len(quoteID) > 64 {
		writeError(w, http.StatusBadRequest, "invalid quote ID")
		return
	}

	quote, err := api.mintStore.GetMintQuote(quoteID)
	if err != nil {
		writeError(w, http.StatusNotFound, "quote not found")
		return
	}

	// If still unpaid, check with Lightning node.
	// If circuit breaker is open, skip the LN check and return cached state.
	if quote.State == ecash.QuoteUnpaid && api.lnc != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		inv, err := api.lnc.LookupInvoice(ctx, quote.PaymentHash)
		if err == nil && inv.Status == lightning.InvoiceSettled {
			_ = api.mintStore.UpdateMintQuoteState(quoteID, ecash.QuotePaid)
			quote.State = ecash.QuotePaid
		}
		// If circuit open or other error, just return cached state — no 503.
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(quote)
}

// handleMintTokens issues blinded signatures for a paid quote (NUT-04).
func (api *DiscoveryAPI) handleMintTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	var req struct {
		Quote   string                `json:"quote"`
		Outputs cashu.BlindedMessages `json:"outputs"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Quote == "" {
		writeError(w, http.StatusBadRequest, "quote ID required")
		return
	}
	if len(req.Outputs) == 0 {
		writeError(w, http.StatusBadRequest, "outputs required")
		return
	}
	if len(req.Outputs) > maxMintOutputs {
		writeError(w, http.StatusBadRequest, "too many outputs (max 64)")
		return
	}

	// Check quote payment status with LN node before minting.
	// If circuit is open, skip — rely on cached state.
	quote, err := api.mintStore.GetMintQuote(req.Quote)
	if err != nil {
		writeError(w, http.StatusNotFound, "quote not found")
		return
	}
	if quote.State == ecash.QuoteUnpaid && api.lnc != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		inv, err := api.lnc.LookupInvoice(ctx, quote.PaymentHash)
		if err == nil && inv.Status == lightning.InvoiceSettled {
			_ = api.mintStore.UpdateMintQuoteState(req.Quote, ecash.QuotePaid)
		}
	}

	// Use worker pool for CPU-bound blind signing.
	ctx, cancel := context.WithTimeout(r.Context(), cryptoTimeout)
	defer cancel()

	sigs, err := api.cryptoPool.MintTokens(ctx, req.Quote, req.Outputs)
	if err != nil {
		switch err {
		case ecash.ErrQuoteNotFound:
			writeError(w, http.StatusNotFound, err.Error())
		case ecash.ErrQuoteNotPaid:
			writeError(w, http.StatusPaymentRequired, err.Error())
		case ecash.ErrQuoteAlreadyUsed:
			writeError(w, http.StatusConflict, err.Error())
		case ecash.ErrOutputOverQuote:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[ecash] mint tokens error: %v", err)
			writeError(w, http.StatusInternalServerError, "mint error")
		}
		return
	}

	resp := struct {
		Signatures cashu.BlindedSignatures `json:"signatures"`
	}{Signatures: sigs}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleSwap exchanges input proofs for new blinded signatures (NUT-03).
func (api *DiscoveryAPI) handleSwap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	var req struct {
		Inputs  cashu.Proofs          `json:"inputs"`
		Outputs cashu.BlindedMessages `json:"outputs"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Inputs) == 0 {
		writeError(w, http.StatusBadRequest, "inputs required")
		return
	}
	if len(req.Outputs) == 0 {
		writeError(w, http.StatusBadRequest, "outputs required")
		return
	}
	if len(req.Inputs) > maxSwapInputs {
		writeError(w, http.StatusBadRequest, "too many inputs (max 64)")
		return
	}
	if len(req.Outputs) > maxSwapOutputs {
		writeError(w, http.StatusBadRequest, "too many outputs (max 64)")
		return
	}

	// Use worker pool for CPU-bound verification + signing.
	ctx, cancel := context.WithTimeout(r.Context(), cryptoTimeout)
	defer cancel()

	sigs, err := api.cryptoPool.Swap(ctx, req.Inputs, req.Outputs)
	if err != nil {
		switch err {
		case ecash.ErrInvalidProof:
			writeError(w, http.StatusBadRequest, err.Error())
		case ecash.ErrProofAlreadySpent:
			writeError(w, http.StatusConflict, err.Error())
		case ecash.ErrDuplicateProofs:
			writeError(w, http.StatusBadRequest, err.Error())
		case ecash.ErrAmountMismatch:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[ecash] swap error: %v", err)
			writeError(w, http.StatusInternalServerError, "swap error")
		}
		return
	}

	resp := struct {
		Signatures cashu.BlindedSignatures `json:"signatures"`
	}{Signatures: sigs}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleCheckState checks whether proofs are spent or unspent (NUT-07).
func (api *DiscoveryAPI) handleCheckState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if api.cashuRateCheck(w, r) == "" {
		return
	}

	var req struct {
		Ys []string `json:"Ys"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Ys) == 0 {
		writeError(w, http.StatusBadRequest, "Ys required")
		return
	}
	if len(req.Ys) > maxCheckStateYs {
		writeError(w, http.StatusBadRequest, "too many Ys (max 100)")
		return
	}

	states, err := api.mint.CheckProofsState(req.Ys)
	if err != nil {
		log.Printf("[ecash] check state error: %v", err)
		writeError(w, http.StatusInternalServerError, "check state error")
		return
	}

	resp := struct {
		States []ecash.ProofState `json:"states"`
	}{States: states}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeError sends a JSON error response (Cashu error format).
func writeError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"detail": detail,
		"code":   status,
	})
}
