package wallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/elnosh/gonuts/cashu"
	gcrypto "github.com/elnosh/gonuts/crypto"
)

// secretBytes is the entropy per token secret. NUT-00 requires an
// unpredictable secret; 32 bytes matches the reference wallets.
const secretBytes = 32

// blindState is the client-side material needed to unblind one signature.
// It must never be sent to the hub: the secret identifies the proof and the
// blinding factor r is what makes the signature unlinkable.
type blindState struct {
	secret string
	r      *secp256k1.PrivateKey
	amount uint64
}

// blindAmounts creates one blinded message per requested denomination.
//
// The secret is generated from crypto/rand independently of the blinding
// factor. Deriving one from the other would let anyone who learns a secret
// recompute r and strip the blinding, destroying unlinkability.
func blindAmounts(keysetID string, amounts []uint64) (cashu.BlindedMessages, []blindState, error) {
	if keysetID == "" {
		return nil, nil, fmt.Errorf("keyset ID is required")
	}

	messages := make(cashu.BlindedMessages, 0, len(amounts))
	states := make([]blindState, 0, len(amounts))

	for _, amount := range amounts {
		secret, err := generateSecret()
		if err != nil {
			return nil, nil, err
		}

		r, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return nil, nil, fmt.Errorf("generate blinding factor: %w", err)
		}

		blinded, _, err := gcrypto.BlindMessage(secret, r)
		if err != nil {
			return nil, nil, fmt.Errorf("blind message: %w", err)
		}

		messages = append(messages, cashu.NewBlindedMessage(keysetID, amount, blinded))
		states = append(states, blindState{secret: secret, r: r, amount: amount})
	}

	return messages, states, nil
}

// generateSecret returns a fresh random Cashu secret.
func generateSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// unblindSignatures converts the hub's blinded signatures into spendable proofs.
//
// For each signature it computes C = C_ - r*K, where K is the mint's public
// key for that denomination. The result is a proof the hub can verify but
// cannot associate with the original mint request.
func unblindSignatures(
	signatures cashu.BlindedSignatures,
	states []blindState,
	keyset *Keyset,
) (cashu.Proofs, error) {
	if len(signatures) != len(states) {
		return nil, fmt.Errorf("got %d signatures for %d blinded messages",
			len(signatures), len(states))
	}

	proofs := make(cashu.Proofs, 0, len(signatures))

	for i, sig := range signatures {
		state := states[i]

		// The hub must sign the denomination we asked for. A mismatch means the
		// proof is worth less than we paid, so fail instead of storing it.
		if sig.Amount != state.amount {
			return nil, fmt.Errorf("signature %d has amount %d, expected %d",
				i, sig.Amount, state.amount)
		}

		blindedSig, err := parsePubkeyHex(sig.C_)
		if err != nil {
			return nil, fmt.Errorf("parse signature %d (C_): %w", i, err)
		}

		mintKeyHex, ok := keyset.Keys[state.amount]
		if !ok {
			return nil, fmt.Errorf("keyset %s has no key for denomination %d",
				keyset.ID, state.amount)
		}
		mintKey, err := parsePubkeyHex(mintKeyHex)
		if err != nil {
			return nil, fmt.Errorf("parse mint key for denomination %d: %w", state.amount, err)
		}

		// C = C_ - r*K
		unblinded := gcrypto.UnblindSignature(blindedSig, state.r, mintKey)

		proofs = append(proofs, cashu.Proof{
			Amount: state.amount,
			Id:     keyset.ID,
			Secret: state.secret,
			C:      hex.EncodeToString(unblinded.SerializeCompressed()),
		})
	}

	return proofs, nil
}

// parsePubkeyHex decodes a compressed secp256k1 public key in hex form.
func parsePubkeyHex(value string) (*secp256k1.PublicKey, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	key, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	return key, nil
}

// Wallet mints and stores Cashu proofs for a single hub.
//
// A Wallet is bound to one hub because proofs are only spendable at the mint
// that issued them. The desktop app creates one Wallet per configured hub.
type Wallet struct {
	client *MintClient
	store  ProofStore
}

// NewWallet creates a wallet backed by the given hub client and proof store.
func NewWallet(client *MintClient, store ProofStore) (*Wallet, error) {
	if client == nil {
		return nil, fmt.Errorf("mint client is required")
	}
	if store == nil {
		return nil, fmt.Errorf("proof store is required")
	}
	return &Wallet{client: client, store: store}, nil
}

// HubURL returns the hub this wallet mints from.
func (w *Wallet) HubURL() string { return w.client.HubURL() }

// RequestQuote asks the hub for an invoice covering amountSats. The caller is
// responsible for paying the returned BOLT11 request.
func (w *Wallet) RequestQuote(ctx context.Context, amountSats uint64) (*MintQuote, error) {
	return w.client.CreateMintQuote(ctx, amountSats)
}

// QuoteStatus reports the latest known state of a quote.
func (w *Wallet) QuoteStatus(ctx context.Context, quoteID string) (*MintQuote, error) {
	return w.client.MintQuoteStatus(ctx, quoteID)
}

// AwaitPayment polls until the invoice is paid, the quote expires, or the
// context is cancelled. Callers should pass a context with a deadline or
// cancel it when the user abandons the purchase.
func (w *Wallet) AwaitPayment(ctx context.Context, quoteID string, interval time.Duration) (*MintQuote, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		quote, err := w.client.MintQuoteStatus(ctx, quoteID)
		if err != nil {
			return nil, err
		}
		if quote.Paid() {
			return quote, nil
		}
		if quote.State == QuoteExpired {
			return nil, fmt.Errorf("mint quote %s expired before payment", quoteID)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Mint exchanges a paid quote for proofs and adds them to the store.
//
// The quote amount is split into power-of-two denominations so the resulting
// proofs can be spent in flexible combinations without a swap.
func (w *Wallet) Mint(ctx context.Context, quote *MintQuote) (cashu.Proofs, error) {
	if quote == nil {
		return nil, fmt.Errorf("quote is required")
	}
	if quote.Amount == 0 {
		return nil, fmt.Errorf("quote %s has zero amount", quote.ID)
	}

	keyset, err := w.client.ActiveKeyset(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch hub keyset: %w", err)
	}

	amounts := cashu.AmountSplit(quote.Amount)
	outputs, states, err := blindAmounts(keyset.ID, amounts)
	if err != nil {
		return nil, err
	}

	signatures, err := w.client.MintTokens(ctx, quote.ID, outputs)
	if err != nil {
		return nil, err
	}

	proofs, err := unblindSignatures(signatures, states, keyset)
	if err != nil {
		return nil, err
	}

	// Persist before returning: proofs represent paid-for bandwidth, so losing
	// them to a crash between mint and save would cost the user real sats.
	if err := w.store.Add(w.client.HubURL(), proofs); err != nil {
		return nil, fmt.Errorf("save minted proofs: %w", err)
	}

	return proofs, nil
}

// Balance returns the total unspent value held for this hub, in sats.
func (w *Wallet) Balance() (uint64, error) {
	proofs, err := w.store.List(w.client.HubURL())
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, proof := range proofs {
		total += proof.Amount
	}
	return total, nil
}

// Proofs returns all unspent proofs held for this hub.
func (w *Wallet) Proofs() (cashu.Proofs, error) {
	return w.store.List(w.client.HubURL())
}

// Reserve removes proofs worth at least amountSats from the store and returns
// them for spending.
//
// Proofs are removed up front because a spent proof is worthless: keeping them
// after handing them to a node risks presenting them again and being rejected
// as a double-spend.
func (w *Wallet) Reserve(amountSats uint64) (cashu.Proofs, error) {
	return w.store.Take(w.client.HubURL(), amountSats)
}

// Release returns previously reserved proofs to the store. Call this when a
// connection attempt fails before the proofs were presented to a node, so the
// user does not lose bandwidth they already paid for.
func (w *Wallet) Release(proofs cashu.Proofs) error {
	if len(proofs) == 0 {
		return nil
	}
	return w.store.Add(w.client.HubURL(), proofs)
}
