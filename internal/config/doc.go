// Package config reads cubit.config/v0: strict YAML, checked against the
// embedded JSON Schema, with every problem reported at once and tied to a line.
// The checks a schema cannot say (duplicate aliases, overlapping roots, refused
// prefixes) are Check, which is what `cubit validate` adds.
package config
