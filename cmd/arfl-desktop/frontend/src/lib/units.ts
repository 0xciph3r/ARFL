// Bandwidth is sold as 100 MB tokens. The hub grants 1 MB per sat of ecash, so
// one token is backed by exactly 100 sats of proofs.
export const TOKEN_MB = 100
export const MB_PER_SAT = 1
export const TOKEN_SATS = TOKEN_MB / MB_PER_SAT

export const tokensFromSats = (sats: number) => Math.floor(sats / TOKEN_SATS)

export const gbFromSats = (sats: number) => ((sats * MB_PER_SAT) / 1000).toFixed(1)

export const gbFromTokens = (tokens: number) => ((tokens * TOKEN_MB) / 1000).toFixed(1)
