// Package migrations embeds the SQL schema migrations, so the server binary carries its schema.
package migrations

import "embed"

// FS holds every NNNN_name.up.sql and NNNN_name.down.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
