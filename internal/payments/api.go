package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Radi-Labs/ARFL/internal/credentials"
	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// PurchaseAPI handles the bandwidth purchase flow:
//
//  1. POST /purchase      — client selects tier, Hub creates Lightning invoice
//  2. GET  /purchase/:id  — client polls; when settled, tickets are delivered
//  3. POST /report        — nodes submit signed usage reports
//
// The flow is asynchronous: the client creates a purchase, pays the invoice
// externally, and polls until the Hub detects settlement and issues tickets.
type PurchaseAPI struct {
	store  *store.Store
	lnc    lightning.Client
	issuer credentials.Issuer
	mux    *http.ServeMux

	// Rate limiting: map[IP][]timestamp.
	rateLimit   map[string][]time.Time
	rateMu      sync.Mutex
	maxRequests int
	rateWindow  time.Duration

	// Serializes ticket issuance to prevent double-issuance from
	// concurrent settlement events for the same invoice.
	issueMu sync.Mutex

	// Invoice settlement subscriber — started once.
	cancelSub context.CancelFunc
}

// NewPurchaseAPI wires the payment endpoints.
func NewPurchaseAPI(s *store.Store, lnc lightning.Client, issuer credentials.Issuer) *PurchaseAPI {
	api := &PurchaseAPI{
		store:       s,
		lnc:         lnc,
		issuer:      issuer,
		mux:         http.NewServeMux(),
		rateLimit:   make(map[string][]time.Time),
		maxRequests: 10, // 10 invoices per hour per IP (economic invariant 15).
		rateWindow:  1 * time.Hour,
	}

	api.mux.HandleFunc("/purchase", api.handlePurchase)
	api.mux.HandleFunc("/purchase/", api.handlePurchaseStatus)
	api.mux.HandleFunc("/report", api.handleReport)

	return api
}

// Handler returns the HTTP handler.
func (api *PurchaseAPI) Handler() http.Handler {
	return api.mux
}

// StartSettlementListener subscribes to Lightning invoice settlements and
// issues tickets when invoices are paid. Call this once at Hub startup.
func (api *PurchaseAPI) StartSettlementListener(ctx context.Context) error {
	subCtx, cancel := context.WithCancel(ctx)
	api.cancelSub = cancel

	ch, err := api.lnc.SubscribeInvoices(subCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("subscribe invoices: %w", err)
	}

	go func() {
		for inv := range ch {
			if inv.Status != lightning.InvoiceSettled {
				continue
			}
			if err := api.onInvoiceSettled(inv); err != nil {
				log.Printf("[payment-api] settlement processing error: %v", err)
			}
		}
	}()

	return nil
}

// Stop cancels the settlement listener.
func (api *PurchaseAPI) Stop() {
	if api.cancelSub != nil {
		api.cancelSub()
	}
}

// --- Purchase request/response types ---

// PurchaseRequest is the JSON body for POST /purchase.
type PurchaseRequest struct {
	TierID string `json:"tier_id"` // "1gb", "10gb", "50gb"
}

// PurchaseResponse is the JSON response for POST /purchase.
type PurchaseResponse struct {
	PaymentHash    string `json:"payment_hash"`
	PaymentRequest string `json:"payment_request"` // BOLT11 invoice
	AmountSats     int64  `json:"amount_sats"`
	Tier           string `json:"tier"`
	ExpiresAt      string `json:"expires_at"`
}

// PurchaseStatusResponse is the JSON response for GET /purchase/:id.
type PurchaseStatusResponse struct {
	PaymentHash string                `json:"payment_hash"`
	Status      string                `json:"status"` // "open", "settled", "expired"
	AmountSats  int64                 `json:"amount_sats"`
	Tier        string                `json:"tier"`
	Tickets     []*credentials.Ticket `json:"tickets,omitempty"` // only when settled
}

// ReportRequest is the JSON body for POST /report.
type ReportRequest struct {
	SessionID     string `json:"session_id"`
	TicketID      string `json:"ticket_id"`
	NodePubkey    string `json:"node_pubkey"`
	NodeRole      string `json:"node_role"`
	BytesReported int64  `json:"bytes_reported"`
	ReportedAt    string `json:"reported_at"`
	Signature     string `json:"signature"`
}

// --- Handlers ---

// handlePurchase creates a new bandwidth purchase.
// POST /purchase {"tier_id": "1gb"}
func (api *PurchaseAPI) handlePurchase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientIP := extractIP(r)
	if !api.checkRateLimit(clientIP) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var req PurchaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	tier, err := credentials.LookupTier(req.TierID)
	if err != nil {
		jsonError(w, fmt.Sprintf("unknown tier: %s", req.TierID), http.StatusBadRequest)
		return
	}

	// Create a Lightning invoice via LND.
	expiry := 15 * time.Minute
	memo := fmt.Sprintf("ARFL %s bandwidth", tier.Name)
	inv, err := api.lnc.CreateInvoice(r.Context(), tier.PriceSats, memo, expiry)
	if err != nil {
		log.Printf("[payment-api] CreateInvoice failed: %v", err)
		jsonError(w, "failed to create invoice", http.StatusInternalServerError)
		return
	}

	// Record in database.
	if err := api.store.InsertInvoice(
		inv.PaymentHash, inv.PaymentRequest,
		tier.PriceSats, tier.ID, tier.Bytes,
		inv.ExpiresAt, clientIP,
	); err != nil {
		log.Printf("[payment-api] InsertInvoice failed: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := PurchaseResponse{
		PaymentHash:    inv.PaymentHash,
		PaymentRequest: inv.PaymentRequest,
		AmountSats:     tier.PriceSats,
		Tier:           tier.ID,
		ExpiresAt:      inv.ExpiresAt.UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// handlePurchaseStatus returns the status of a purchase and delivers tickets
// when the invoice has been settled.
// GET /purchase/{payment_hash}
func (api *PurchaseAPI) handlePurchaseStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract payment hash from URL: /purchase/abc123
	path := strings.TrimPrefix(r.URL.Path, "/purchase/")
	paymentHash := strings.TrimSpace(path)
	if paymentHash == "" {
		jsonError(w, "missing payment hash", http.StatusBadRequest)
		return
	}

	inv, err := api.store.GetInvoice(paymentHash)
	if err != nil {
		jsonError(w, "purchase not found", http.StatusNotFound)
		return
	}

	resp := PurchaseStatusResponse{
		PaymentHash: inv.PaymentHash,
		Status:      inv.Status,
		AmountSats:  inv.AmountSats,
		Tier:        inv.Tier,
	}

	// If settled, fetch tickets.
	if inv.Status == "settled" {
		tickets, err := api.store.GetTicketsByPaymentHash(paymentHash)
		if err != nil {
			log.Printf("[payment-api] GetTickets failed: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		resp.Tickets = ticketRecordsToCredentials(tickets)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleReport receives signed usage reports from nodes.
// POST /report
func (api *PurchaseAPI) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Reconstruct and verify the usage report signature.
	report := &UsageReport{
		SessionID:     req.SessionID,
		TicketID:      req.TicketID,
		NodePubkey:    req.NodePubkey,
		NodeRole:      req.NodeRole,
		BytesReported: req.BytesReported,
		ReportedAt:    req.ReportedAt,
		Signature:     req.Signature,
	}

	if err := report.Verify(); err != nil {
		jsonError(w, fmt.Sprintf("report verification failed: %v", err), http.StatusBadRequest)
		return
	}

	// Store the verified report.
	if err := api.store.InsertUsageReport(
		req.SessionID, req.TicketID, req.NodePubkey, req.NodeRole,
		req.BytesReported, req.ReportedAt, req.Signature,
	); err != nil {
		log.Printf("[payment-api] InsertUsageReport failed: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// --- Settlement callback ---

// onInvoiceSettled is called when a Lightning invoice is paid.
// It issues tickets and stores them in the database.
//
// Crash-safe: ticket insertion uses a transaction — crash mid-insert
// rolls back, and the next call re-issues all tickets (count==0).
//
// Concurrent-safe: issueMu serializes ticket issuance per Hub instance,
// preventing double-issuance from concurrent settlement events.
func (api *PurchaseAPI) onInvoiceSettled(inv *lightning.Invoice) error {
	// Serialize ticket issuance — prevents concurrent settlement events
	// for the same invoice from both issuing tickets (2N tickets).
	api.issueMu.Lock()
	defer api.issueMu.Unlock()

	// Step 1: Check if tickets already exist (idempotency guard).
	existing, err := api.store.CountTicketsByPaymentHash(inv.PaymentHash)
	if err != nil {
		return fmt.Errorf("count tickets for %s: %w", inv.PaymentHash, err)
	}
	if existing > 0 {
		log.Printf("[payment-api] tickets already issued for %s, skipping", inv.PaymentHash)
		return nil
	}

	// Step 2: Mark invoice as settled (idempotent — returns nil if already settled).
	if err := api.store.SettleInvoice(inv.PaymentHash); err != nil {
		return fmt.Errorf("settle invoice %s: %w", inv.PaymentHash, err)
	}

	// Step 3: Look up the invoice to get tier info.
	record, err := api.store.GetInvoice(inv.PaymentHash)
	if err != nil {
		return fmt.Errorf("get invoice %s: %w", inv.PaymentHash, err)
	}

	tier, err := credentials.LookupTier(record.Tier)
	if err != nil {
		return fmt.Errorf("lookup tier %s: %w", record.Tier, err)
	}

	// Step 4: Issue tickets in memory.
	tickets, err := api.issuer.Issue(inv.PaymentHash, tier.TicketBytes, tier.TicketCount)
	if err != nil {
		return fmt.Errorf("issue tickets for %s: %w", inv.PaymentHash, err)
	}

	// Step 5: Insert all tickets atomically (transaction).
	// If Hub crashes mid-insert, the transaction rolls back and the
	// next call will see count==0 and re-issue all tickets.
	if err := api.store.InsertTicketsBatch(inv.PaymentHash, tickets); err != nil {
		return fmt.Errorf("insert tickets for %s: %w", inv.PaymentHash, err)
	}

	log.Printf("[payment-api] issued %d tickets for invoice %s (tier %s)",
		len(tickets), inv.PaymentHash, record.Tier)
	return nil
}

// --- Helpers ---

func extractIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (api *PurchaseAPI) checkRateLimit(clientIP string) bool {
	api.rateMu.Lock()
	defer api.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-api.rateWindow)

	timestamps := api.rateLimit[clientIP]
	var fresh []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			fresh = append(fresh, ts)
		}
	}

	if len(fresh) >= api.maxRequests {
		return false
	}

	api.rateLimit[clientIP] = append(fresh, now)
	return true
}

// ticketRecordsToCredentials converts DB ticket records back to credential Tickets.
// This is what the client receives after payment — the actual redeemable tickets.
func ticketRecordsToCredentials(records []store.TicketRecord) []*credentials.Ticket {
	tickets := make([]*credentials.Ticket, len(records))
	for i, r := range records {
		tickets[i] = &credentials.Ticket{
			ID:    r.ID,
			Bytes: r.BytesValue,
			MAC:   r.HMAC,
		}
	}
	return tickets
}
