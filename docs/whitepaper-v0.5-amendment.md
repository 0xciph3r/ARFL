# Draft v0.5 payment and quota amendment (proposed)

Status: **working direction approved; not ratified or implemented**. This
document identifies changes to `ARFL_Whitepaper_v0.4.pdf` for review before
protocol code is merged. Draft v0.4 remains the published description until
the revised paper is approved.

## Why a revision is needed

Draft v0.4, Section 6 and Table 1, describe independently priced, discounted
hub tiers issued as 100 MB Cashu bandwidth tokens. Sections 6.2 and 6.4
describe sat-denominated mint quotes and payouts based on signed usage. The
current desktop instead buys arbitrary sat-denominated Cashu proofs at a fixed
conversion of 1 sat to 1 MB, and Cashu redemptions do not feed the older
ticket-based settlement ledger. Showing the paper's example tier prices in
this desktop flow would misrepresent both the bandwidth purchased and node
earnings.

Draft v0.4 also says that partly consuming a 100 MB token forfeits its unused
remainder. The proposed policy below reverses that rule. Neither policy
should be presented as implemented until the corresponding tests and
deployment exist.

## Proposed purchase and redemption terms

1. Each hub publishes its actual tiers, their price in sats, bandwidth in
   bytes, credit denomination, expiry, and refund policy. For a 100 MB credit
   denomination, a tier price must divide evenly by its credit count: the
   price per credit is an integer number of sats. A hub may choose any
   positive price satisfying this accounting constraint, and an invalid
   tier must be rejected rather than rounded or advertised incorrectly.
   The terms accepted
   by the buyer must be bound to the Lightning quote; a later change to
   `/info` must not change the paid quote or the amount minted.
2. Blind proofs remain denominated in sats and are tagged by tier, for
   example through a distinct mint keyset. A 100 MB credit costs the
   integer per-credit sat price for that tier. A credit may require
   multiple power-of-two Cashu proofs to represent that price, so the mint
   must issue the exact paid number of credits and redemption must accept
   only whole credits. Swaps must stay within a tier and must not turn
   discounted credits into more expensive tiers. Existing sat-denominated
   proofs remain redeemable under their original conversion, or migration
   must be explicitly funded and verified before removing that path.
3. A node presents proofs to the hub, which atomically rejects double spends
   and returns the exact quota and its tier. Node identity and the resulting
   usage reports must be authenticated. The present unauthenticated
   `node_pubkey` field on `/v1/redeem` is **not** evidence of who served
   traffic.
4. Node earnings accrue from signed usage reports for the entry and exit
   serving a session, capped by both the prepaid quota and the lesser of
   the two reported byte counts. The hub uses the rate applicable to those
   credits, deducts its configured margin, and divides the remainder equally
   between the two nodes, as Section 6.4 describes. Redeeming a proof alone
   is not a payout, and paying for unused bytes would change this policy.
5. Settlement must be bounded by funds received for the relevant tier and
   remain idempotent across retries. The hub must not rely solely on a
   self-reported client identity or allow the same used bytes to earn twice.

**Privacy change:** A tier tag lets a node and the hub distinguish which
price class a redeemed proof belongs to. Blind signatures still prevent a
direct match to its Lightning quote, but a rare tier, purchase timing, and
redemption timing can reduce the anonymity set. This is weaker than claiming
all purchases are indistinguishable and belongs in the threat model and
privacy matrix. Matching the two usage reports also reveals the entry/exit
pair to the hub at settlement time. Draft v0.5 must say so plainly: Cashu
protects direct buyer-to-redemption linkage, **not** hub-blind topology.
The hub also sees the buyer's network address when contacted directly for
purchase; timing may allow correlation despite cryptographic blinding.

## Proposed remaining-bandwidth policy

The paid but unused part of a redeemed credit survives an ordinary
disconnect, a reconnect to the same node, and a node restart until the
published expiry. The node
stores a durable, monotonic record of granted and consumed bytes, restores
enforcement before accepting traffic, and never grants the same remaining
bytes twice. A client needs a verifiable way to resume its claim even if
its per-session WireGuard key changes; the hub must not learn the entry/exit
pair through this resume mechanism.

A hub must publish its configurable lifetime and refund policy before
purchase, and the quote must bind those terms. The client must display the
expiry and explain the treatment of unused bytes before payment. A node
going permanently offline is not the same as an ordinary restart: a refund
requires an independently enforceable funding and dispute mechanism, not
just an app warning. Until such a mechanism exists, the client must not
promise a refund or silently claim that the credits are recoverable.

This replaces the partial-token forfeiture rule in Draft v0.4, Table 1;
the final paper must update that table and Sections 5.3, 6.2, 6.3, 6.4,
the failure cases, and the privacy/threat model together.

## Design decisions required before implementation

- Specify how a client receives and spends whole 100 MB credits represented
  by multiple power-of-two sat proofs, respecting mint/output limits.
  Define keyset rotation and the fate of outstanding credits when a tier
  is removed. Hub tier prices not divisible by their credit count are
  invalid, not rounded.
- Define authenticated attribution of measured usage to a tier and the two
  serving nodes without a buyer identifier. The hub **may see the pair**;
  specify the reporting cadence, counter resets, disputed reports, the
  colluding-node threat, and reconciliation of unspent liability against
  cash held by the hub.
- Define the portable resume receipt or equivalent proof of remaining
  credit; its replay, theft, key rotation, and node-loss behavior need tests.
- Specify allowable expiry/refund policies, who funds refunds if a node
  disappears, and what happens when a hub disappears. A configurable term
  without enforcement is only a disclosure, not a guarantee.
- Decide how older fixed-rate Cashu proofs and wallet backups are migrated
  without reinterpreting their existing value.

No discounted-tier quote should be offered by the desktop until issuance,
redemption, durable quota, usage-based settlement, migration, and the
displayed purchase terms agree in end-to-end tests.
