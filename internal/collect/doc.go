// Package collect turns finished scan documents into the Prometheus text
// exposition that `cubit serve` publishes. It is the only place the metric
// names are spelled, and a test holds them to docs/metrics.md.
//
// A scrape never walks: every scan renders one immutable snapshot, and the
// HTTP handler hands out whichever snapshot is current.
package collect
