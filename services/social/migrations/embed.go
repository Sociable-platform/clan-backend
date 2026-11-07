// Package migrations holds the social service's SQL migrations, built into the binary.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
