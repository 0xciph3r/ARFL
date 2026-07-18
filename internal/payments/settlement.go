package payments

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Radi-Labs/ARFL/internal/lightning"
	"github.com/Radi-Labs/ARFL/internal/store"
)

// ErrPaymentInFlight is returned when a Lightning payment is still routing.
// The payout stays in in_flight state for manual reconciliation.
var ErrPaymentInFlight = errors.New("payment in-flight")

// SettlementEngine computes and executes node payouts.
//
// Every settlement period (default 6h), it:
//  1. Aggregates usage reports into billable bytes per node
//  2. Deducts the hub margin, then splits remaining sats 50/50 entry/exit
//  3. Creates settlement entries (idempotent by period+node)
//  4. Creates and executes payouts via Lightning Keysend
//
// The engine is crash-safe: all state transitions are persisted before
// network calls, and the in_flight status marks the danger zone.
type SettlementEngine struct {
	store        *store.Store
	lnc          lightning.Client
	minPayout    int64 // minimum sats before payout (default 1000)
	hubMarginPct int   // percentage hub retains (default 20)
}

// NewSettlementEngine creates a settlement engine.
func NewSettlementEngine(s *store.Store, lnc lightning.Client) *SettlementEngine {
	return &SettlementEngine{
		store:        s,
		lnc:          lnc,
		minPayout:    1000,
		hubMarginPct: 20,
	}
}

// SetMinPayout overrides the minimum payout threshold (for testing).
func (e *SettlementEngine) SetMinPayout(sats int64) {
	e.minPayout = sats
}

// SetHubMargin sets the hub's revenue percentage (0-50).
func (e *SettlementEngine) SetHubMargin(pct int) {
	if pct < 0 || pct > 50 {
		pct = 20
	}
	e.hubMarginPct = pct
}

// SettlementResult summarizes what happened in a settlement run.
type SettlementResult struct {
	Period           string
	SessionsSettled  int
	SessionsSkipped  int // one-sided reports
	EntriesCreated   int
	PayoutsSent      int
	PayoutsSucceeded int
	PayoutsFailed    int
	PayoutsInFlight  int // still routing — requires manual reconciliation
	TotalPaidSats    int64
}

// RunSettlement executes one settlement cycle for a given period.
// periodStart and periodEnd are RFC3339 timestamps.
// This method is idempotent — running it twice for the same period is safe.
func (e *SettlementEngine) RunSettlement(ctx context.Context, periodStart, periodEnd string) (*SettlementResult, error) {
	period := periodStart + "/" + periodEnd
	result := &SettlementResult{Period: period}

	// Step 1: Get session usage summaries (only sessions with both entry+exit).
	summaries, err := e.store.GetSessionUsageSummaries(periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("get session summaries: %w", err)
	}

	// Step 2: Aggregate by node — each node gets one settlement entry per period.
	type nodeAgg struct {
		entryBytesTotal int64
		exitBytesTotal  int64
		billableBytes   int64
		billableSats    int64 // tier-aware sats using invoice rate
		ticketsRedeemed int
	}
	nodeAggs := make(map[string]*nodeAgg)

	for _, s := range summaries {
		// Validate the ticket chain.
		info, err := e.store.GetTicketSettlementInfo(s.TicketID)
		if err != nil {
			log.Printf("[settlement] skip session %s: ticket lookup failed: %v", s.SessionID, err)
			result.SessionsSkipped++
			continue
		}
		if info.TicketStatus != "redeemed" {
			log.Printf("[settlement] skip session %s: ticket %s not redeemed (status: %s)",
				s.SessionID, s.TicketID, info.TicketStatus)
			result.SessionsSkipped++
			continue
		}
		if info.InvoiceStatus != "settled" {
			log.Printf("[settlement] skip session %s: invoice not settled", s.SessionID)
			result.SessionsSkipped++
			continue
		}
		if info.BytesAllowed == 0 {
			log.Printf("[settlement] skip session %s: invoice bytes_allowed is zero", s.SessionID)
			result.SessionsSkipped++
			continue
		}

		// Billable bytes = min(entry, exit). Cap at ticket's purchased bytes.
		billable := min64(s.EntryBytes, s.ExitBytes)
		if billable > info.TicketBytes {
			billable = info.TicketBytes
		}

		// Compute sats per node share using the invoice's rate (immune to tier config changes).
		// Deduct hub margin first, then split remaining 50/50 between entry and exit.
		totalSats := computePayoutSats(billable, info.AmountSats, info.BytesAllowed)
		nodePool := totalSats * int64(100-e.hubMarginPct) / 100
		entrySats := nodePool / 2
		exitSats := nodePool - entrySats // avoids rounding loss

		// Aggregate for entry node.
		agg := nodeAggs[s.EntryNode]
		if agg == nil {
			agg = &nodeAgg{}
			nodeAggs[s.EntryNode] = agg
		}
		agg.entryBytesTotal += s.EntryBytes
		agg.exitBytesTotal += s.ExitBytes
		agg.billableBytes += billable
		agg.billableSats += entrySats
		agg.ticketsRedeemed++

		// Aggregate for exit node.
		agg2 := nodeAggs[s.ExitNode]
		if agg2 == nil {
			agg2 = &nodeAgg{}
			nodeAggs[s.ExitNode] = agg2
		}
		agg2.entryBytesTotal += s.EntryBytes
		agg2.exitBytesTotal += s.ExitBytes
		agg2.billableBytes += billable
		agg2.billableSats += exitSats
		agg2.ticketsRedeemed++

		result.SessionsSettled++
	}

	// Step 3: Create settlement entries per node (idempotent via INSERT OR IGNORE).
	for nodePubkey, agg := range nodeAggs {
		inserted, err := e.store.InsertSettlementEntry(
			period, nodePubkey, agg.billableBytes, agg.billableSats,
			agg.entryBytesTotal, agg.exitBytesTotal, agg.ticketsRedeemed,
		)
		if err != nil {
			log.Printf("[settlement] insert entry for %s: %v", nodePubkey, err)
			continue
		}
		if inserted {
			result.EntriesCreated++
		}
	}

	// Step 4: Execute payouts for all unsettled entries above threshold.
	if err := e.executePayouts(ctx, result); err != nil {
		return result, fmt.Errorf("execute payouts: %w", err)
	}

	return result, nil
}

// RetryFailedPayouts attempts to resend failed payouts that haven't exceeded max retries.
func (e *SettlementEngine) RetryFailedPayouts(ctx context.Context) (succeeded, failed int, err error) {
	payouts, err := e.store.GetRetryablePayouts()
	if err != nil {
		return 0, 0, fmt.Errorf("get retryable payouts: %w", err)
	}

	for _, p := range payouts {
		if err := e.store.MarkPayoutRetrying(p.ID); err != nil {
			log.Printf("[settlement] retry transition failed for payout %d: %v", p.ID, err)
			continue
		}

		if err := e.sendPayout(ctx, p.ID, p.NodePubkey, p.AmountSats); err != nil {
			failed++
			log.Printf("[settlement] retry failed for payout %d: %v", p.ID, err)
		} else {
			succeeded++
		}
	}
	return succeeded, failed, nil
}

// executePayouts creates and sends payouts for all eligible entries.
func (e *SettlementEngine) executePayouts(ctx context.Context, result *SettlementResult) error {
	// Pre-flight budget check: ensure we're not about to exceed purchases.
	purchased, err := e.store.TotalPurchasedSats()
	if err != nil {
		return fmt.Errorf("get total purchased: %w", err)
	}
	committed, err := e.store.TotalCommittedPayoutSats()
	if err != nil {
		return fmt.Errorf("get total committed: %w", err)
	}

	entries, err := e.store.GetUnsettledEntries(e.minPayout)
	if err != nil {
		return fmt.Errorf("get unsettled entries: %w", err)
	}

	for _, entry := range entries {
		// Budget guard: committed + this payout must not exceed purchased.
		if committed+entry.AmountSats > purchased {
			log.Printf("[settlement] BUDGET HALT: committed %d + proposed %d > purchased %d",
				committed, entry.AmountSats, purchased)
			break
		}

		payoutID, err := e.store.InsertPayout(entry.ID, entry.NodePubkey, entry.AmountSats)
		if err != nil {
			log.Printf("[settlement] insert payout for entry %d: %v", entry.ID, err)
			continue
		}

		// Payout is now committed (pending state) — count it against the
		// budget immediately, regardless of whether sendPayout succeeds.
		// This prevents under-counting when payouts are in-flight or failed.
		committed += entry.AmountSats

		if err := e.sendPayout(ctx, payoutID, entry.NodePubkey, entry.AmountSats); err != nil {
			if errors.Is(err, ErrPaymentInFlight) {
				result.PayoutsInFlight++
			} else {
				result.PayoutsFailed++
			}
			log.Printf("[settlement] payout %d: %v", payoutID, err)
		} else {
			result.PayoutsSucceeded++
			result.TotalPaidSats += entry.AmountSats
		}
		result.PayoutsSent++
	}
	return nil
}

// sendPayout handles the in_flight → paid/failed transition.
//
// PaymentInFlight is NOT treated as a failure — the payout remains in
// in_flight state for manual reconciliation. Marking an in-flight payment
// as failed would allow a retry that double-pays the node.
func (e *SettlementEngine) sendPayout(ctx context.Context, payoutID int64, nodePubkey string, amountSats int64) error {
	// Pre-flight: if context is already canceled, don't start — no payment was attempted.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before payout: %w", err)
	}

	// Mark in_flight BEFORE the network call — crash safety boundary.
	if err := e.store.MarkPayoutInFlight(payoutID); err != nil {
		return fmt.Errorf("mark in_flight: %w", err)
	}

	// Keysend to the node.
	result, err := e.lnc.Keysend(ctx, nodePubkey, amountSats)
	if err != nil {
		// Context cancellation/deadline after marking in_flight: payment outcome is unknown.
		// The node may have received the payment but the RPC response was lost.
		// Leave payout in in_flight state for manual reconciliation — do NOT mark failed.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[settlement] payout %d: context error after in_flight (%v), leaving in_flight for reconciliation", payoutID, err)
			return ErrPaymentInFlight
		}
		if markErr := e.store.MarkPayoutFailed(payoutID, err.Error()); markErr != nil {
			log.Printf("[settlement] CRITICAL: payout %d keysend error (%v) AND mark-failed error (%v)",
				payoutID, err, markErr)
		}
		return err
	}

	switch result.Status {
	case lightning.PaymentSucceeded:
		return e.store.MarkPayoutPaid(payoutID, result.PaymentHash)
	case lightning.PaymentInFlight:
		// Payment still routing — leave in in_flight state.
		// Do NOT mark as failed; retrying could cause double-payment.
		log.Printf("[settlement] payout %d: payment in-flight, requires reconciliation", payoutID)
		return ErrPaymentInFlight
	default:
		errMsg := "payment did not succeed"
		if result.Error != "" {
			errMsg = result.Error
		}
		if markErr := e.store.MarkPayoutFailed(payoutID, errMsg); markErr != nil {
			log.Printf("[settlement] CRITICAL: payout %d payment failed (%s) AND mark-failed error (%v)",
				payoutID, errMsg, markErr)
		}
		return fmt.Errorf("payment failed: %s", errMsg)
	}
}

// computePayoutSats converts billable bytes to sats using the invoice's rate.
// Uses the originating invoice's amount_sats / bytes_allowed to ensure volume
// discounts are correctly reflected and the rate is immune to config changes.
func computePayoutSats(billableBytes, invoiceAmountSats, invoiceBytesAllowed int64) int64 {
	if invoiceBytesAllowed == 0 {
		return 0
	}
	// Integer division: round down (nodes paid slightly less on fractions).
	return (billableBytes * invoiceAmountSats) / invoiceBytesAllowed
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
