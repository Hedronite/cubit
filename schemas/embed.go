// Package schemas embeds the two JSON Schemas that are the contract of record,
// so the binary validates against exactly the files in this directory.
package schemas

import _ "embed"

// Config is schemas/cubit.config.v0.schema.json.
//
//go:embed cubit.config.v0.schema.json
var Config []byte

// Scan is schemas/cubit.scan.v0.schema.json.
//
//go:embed cubit.scan.v0.schema.json
var Scan []byte

// Validate is schemas/cubit.validate.v0.schema.json.
//
//go:embed cubit.validate.v0.schema.json
var Validate []byte
