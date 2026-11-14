// Package serve runs the exporter: it binds a listener (loopback unless the
// operator opts out), runs scans on an interval, one at a time, hands each
// finished scan to the collector, and answers /metrics and /healthz from the
// last snapshot. A scrape never walks.
package serve
