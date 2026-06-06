package credentials

import "fmt"

// Tier represents a bandwidth purchase plan.
// Tiers define how much bandwidth you get for how many sats.
type Tier struct {
	ID           string // machine-readable identifier
	Name         string // human-readable label
	Bytes        int64  // total bandwidth in bytes
	PriceSats    int64  // cost in satoshis
	TicketCount  int    // how many tickets this tier produces
	TicketBytes  int64  // bytes per ticket (Bytes / TicketCount)
}

// DefaultTiers defines the initial pricing model.
// These are accessible starting points — 500 sats ≈ $0.25 USD.
// The Hub can override these via config.
var DefaultTiers = map[string]Tier{
	"1gb": {
		ID:          "1gb",
		Name:        "1 GB",
		Bytes:       1_000_000_000,                // 1 GB (decimal, matches network convention)
		PriceSats:   500,
		TicketCount: 10,
		TicketBytes: 100_000_000,                  // 100 MB per ticket
	},
	"10gb": {
		ID:          "10gb",
		Name:        "10 GB",
		Bytes:       10_000_000_000,               // 10 GB
		PriceSats:   4_000,
		TicketCount: 100,
		TicketBytes: 100_000_000,                  // 100 MB per ticket
	},
	"50gb": {
		ID:          "50gb",
		Name:        "50 GB",
		Bytes:       50_000_000_000,               // 50 GB
		PriceSats:   15_000,
		TicketCount: 500,
		TicketBytes: 100_000_000,                  // 100 MB per ticket
	},
}

// LookupTier returns the tier for the given ID, or an error if not found.
func LookupTier(id string) (Tier, error) {
	t, ok := DefaultTiers[id]
	if !ok {
		return Tier{}, fmt.Errorf("unknown tier: %s", id)
	}
	return t, nil
}

// SatsPerByte returns the cost per byte for a given tier.
// Used by the settlement engine to calculate node payouts.
func (t Tier) SatsPerByte() float64 {
	return float64(t.PriceSats) / float64(t.Bytes)
}
