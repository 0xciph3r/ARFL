# Trusted hubs

`registry.json` is the list of hubs the ARFL apps show as **Trusted by ARFL**.
Apps read it from the `main` branch, so a hub appears for every user within a
day of its pull request being merged. No app release is needed.

## Add your hub

1. Run a hub (see the main README) behind a domain with a valid TLS
   certificate, and set `"name"` in its `hub.json`.
2. Add an entry to `registry.json`:

   ```json
   {
     "name": "Your Hub",
     "url": "https://hub.example.com",
     "operator": "Who runs it",
     "nostr_pubkey": "<the hub's 64-character hex Nostr public key>",
     "contact": "https://example.com/contact",
     "added": "YYYY-MM-DD"
   }
   ```

3. Check it from the repository root:

   ```
   go run ./cmd/arfl-registry check
   ```

4. Open a pull request. Merging it publishes the hub.

## What the check requires

- `url` is `https://` with a domain name, no IP address and no path.
- The hub answers `/info` over HTTPS and reports the same `name`.
- At least one of its nodes is online.
- No hub is listed twice.

A hub that later fails these checks can be removed by another pull request.
Users who bought tokens there keep them, since tokens are held on their own
devices, but new users will no longer see it.
