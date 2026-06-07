# ARFL — Economic Invariants

These are the non-negotiable rules of the payment system.
Every test, every settlement, every payout must satisfy ALL of these.
If code violates an invariant, the code is wrong — not the invariant.

---

## Ledger Invariants

1. **The settlement ledger is append-only.** No record is ever updated or
   deleted. Balances are derived by summing events, never stored directly.
   If SQLite is corrupted, the append-only ledger is the reconstruction
   source. Think of it like a blockchain — you replay events to get state.

2. **Total node payouts can never exceed total customer purchases.** The Hub
   cannot pay out more sats than it received. This is checked on every
   settlement cycle. If violated, settlement halts and alerts.

3. **Every sat has a traceable path.** For any payout, you can trace:
   customer purchase → ticket issuance → ticket redemption → usage report →
   settlement entry → payout attempt. No gaps.

---

## Ticket Invariants

4. **A ticket can only be redeemed once.** Once a node presents a ticket for
   redemption, it is marked `redeemed` and can never be used again. This is
   the foundation of the entire system — double-spend = system failure.

5. **A ticket is atomic.** A 100MB ticket is either fully consumed or not
   consumed at all. There is no "partial spend." If a user uses 20MB of a
   100MB ticket, the remaining 80MB is forfeit. This wastes at most one
   ticket's worth of bandwidth (~0.5 sats at current rates) but eliminates
   an entire class of accounting bugs and makes Phase 4 blind signatures
   trivially compatible.

6. **Ticket size is configurable.** The default (100MB) is a starting point,
   not a sacred number. Real usage data will tell us if it's too small
   (too many redemptions per session = overhead) or too large (too much
   waste per session = user cost). The protocol constant is tunable without
   code changes.

---

## Settlement Invariants

7. **A node cannot be paid without a redeemed ticket.** No ticket redemption
   = no billable bytes = no payout. Reports without matching tickets are
   logged but ignored for settlement.

8. **Billable bytes = min(entry_bytes, exit_bytes).** One sentence a node
   operator can understand: "You get paid for the lesser of what the entry
   node reported and what the exit node reported." No exceptions, no
   adjustments, no human overrides in v1.

9. **A payout can only reference existing settlement entries.** The payout
   record points to specific settlement entries. You cannot pay a node for
   an amount that doesn't trace back to settled ticket redemptions.

10. **Every settlement operation is idempotent.** Running settlement for the
    same period twice produces the same result. Keyed by (settlement_period,
    node_id). A Hub crash mid-settlement can safely restart without
    double-paying or missing payments.

---

## Payout State Machine

11. **Money is never in an ambiguous state.** Every payout has exactly one
    state at any time:

    ```
    pending → paid
                ↘
    pending → failed → retrying → paid
                                    ↘
    pending → failed → retrying → failed (→ manual review)
    ```

    If a keysend to a node fails, the sats stay in `pending` or `failed`
    state. They are never lost, never double-counted, never in limbo.
    Max 3 retry attempts before manual review flag.

---

## Usage Report Schema

12. **Minimum evidence for payment.** A node must submit exactly this to be
    eligible for settlement:

    ```
    session_id      — which session
    ticket_id       — which ticket was redeemed
    node_role       — "entry" or "exit"
    bytes_reported  — raw bytes served
    timestamp       — when the report was generated
    node_signature  — BIP-340 sig over the above fields
    ```

    No extra fields until proven necessary. The signature prevents spoofing
    (STRIDE/Spoofing) — a node cannot forge reports for another node.

---

## Anti-Abuse Invariants

13. **A ticket cannot be redeemed before it is issued.** Redemption timestamp
    must be >= issuance timestamp. Prevents replay of tickets from the
    future (clock skew tolerance: 30 seconds).

14. **A session cannot consume more tickets than its tier allows.** A 1GB
    purchase = 10 tickets. The Hub rejects ticket redemption #11. This
    prevents credential inflation even if the credential token is leaked.

15. **Invoice creation is rate-limited.** Max 10 unpaid invoices per IP per
    hour. Prevents invoice spam DoS against the Hub's LND node.

---

## Phase 4 Migration Invariant

16. **The credential issuance interface is the ONLY component that changes
    in Phase 4.** Everything downstream — ticket redemption, settlement,
    payouts — stays identical. The blind signature swap is isolated to:
    - How tickets are created (signed → blind-signed)
    - How tickets are verified (HMAC check → blind sig verify)

    If Phase 4 requires changes to settlement or payouts, the interface
    abstraction has failed.

---

## How To Use This Document

- **Before writing code:** Does this feature violate any invariant?
- **Before merging a PR:** Do the tests assert these invariants?
- **During an incident:** Which invariant was violated?
- **During a design review:** Does the new design preserve all invariants?

If you need to change an invariant, that's a design decision — document it
in decisions.md with the reasoning, get it reviewed, and update this file.
