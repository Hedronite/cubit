// Package contract holds the C0 contract tests for Cubit: the two JSON
// Schemas, the metric table, the refuse-list document and the example rules
// must agree with each other. The package has no runtime code. It exists so
// that CI fails when one of those files drifts from the others, before any
// scanner is written against them.
package contract
