package sqlitemigrations

import "embed"

// FS contains SQLite migrations. The migrator applies them in lexical order.
//
//go:embed *.sql
var FS embed.FS
