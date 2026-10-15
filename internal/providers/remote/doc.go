// SPDX-License-Identifier: GPL-2.0-only

// Package remote provides the shared outbound HTTP transport for internet
// metadata providers: a clock-injected token-bucket rate limiter and an
// in-memory response cache that honors upstream Cache-Control/Expires headers.
//
// It exists because Deadfin has no ambient HTTP client — every provider that
// reaches the internet (TMDb in P8-01, others later) shares one [Client] that
// bounds request rate and reuses cacheable responses, so a P10 rescan does not
// re-fetch metadata the provider already retrieved within its freshness window.
//
// The transport is constructed in the composition root and passed down
// explicitly (CONVENTIONS 1.1); its underlying request executor is a swappable
// [Doer] so tests replay recorded cassettes instead of calling live APIs.
package remote
