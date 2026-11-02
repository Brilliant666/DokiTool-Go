# Same-source credential recovery (Issue #16)

P2, controlled liveness defect: a recognized Pipixia TOS source can contain old
and new signed URLs at once. Stable source IDs previously deduplicated these
observations to one URL; unknown expiry gave no trustworthy freshness ranking.

The parser now preserves the first URL and at most two other distinct credential
URLs for that **same normalized host/object/content-query identity**. Input order
is retained, not interpreted as freshness. Unknown, mixed, or changed identities
are never combined. No new default is selected. More than three observations are
bounded; availability of an arbitrary later candidate is not guaranteed.

`Source.fallback_urls` is an optional additive unified-v1 field (one or two HTTPS
URLs, same origin, no credentials or fragments, distinct from the primary).
The current producer emits it only for the proven Pipixia credential group. It
asserts the same logical file, not just matching resolution or URL host; consumers
must not invent this field by collecting arbitrary same-host URLs. Existing
whole-observation ranking, default and raw signed URL bytes remain unchanged.
Old tolerant clients continue using `url` and do not gain recovery; strict schema
clients must consume the updated schema. PHP output/oracles are unchanged.

The file-download consumer puts these credentials immediately after the chosen
primary URL, ahead of existing mirror routes, using the existing three-route
transport cap. Two retained credentials can consume the slots previously available
to other CDN mirrors; preserving the selected source takes priority, not exhaustive
mirror coverage. It only advances after a failed transfer; cancellation stops it.
Known primary size facts are not copied to other credential URLs. This change
covers file downloads, not playback, ZIP, HLS or segment recovery.

Tests: `TestPipixiaCredentialFallback` red before the fix; bounds/identity guards
and `TestCredentialFallbackSourceValidation` cover rejection. The existing
`TestPipixiaRepresentationExport` and `test-pipixia-representation.mjs` CI gate
now also execute four ordering/default/explicit cases, all-failed and cancellation
controls, and host/object/content-query changes (18 new consumer scenarios).
These use actual Go/Vue code with controlled HTTP/DOM boundaries. They are not
real-platform occurrence, browser-native delivery, or independent acceptance.
The implementation requires cross-review by Chat or another executor.
