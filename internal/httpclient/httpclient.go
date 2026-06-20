// Package httpclient holds shared HTTP client configuration for the CLI.
//
// It exists so the request timeout has a single source of truth across the
// cmd and tui packages. Before this, the 10s value was duplicated in
// cmd/worker.go, cmd/context.go, and internal/tui/plan.go, which could
// silently diverge.
package httpclient

import "time"

// Timeout bounds every CLI HTTP call so a server that accepts the connection
// then stalls cannot hang the CLI indefinitely. It is a var (not const) so
// tests can shrink it to keep timeout cases fast.
var Timeout = 10 * time.Second
