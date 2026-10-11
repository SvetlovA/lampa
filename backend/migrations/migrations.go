// Package migrations embeds the lampa-api database schema migrations.
package migrations

import "embed"

// FS holds the goose sql migrations, applied in file name order.
//
//go:embed *.sql
var FS embed.FS
