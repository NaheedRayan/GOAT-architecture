// Package migrations embeds the SQL migrations so the migrate command (and the
// container image) carry them; no external tooling is needed at deploy time.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
