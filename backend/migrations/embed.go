package migrations

import "embed"

// Files contains ordered schema migrations independent of the working directory.
//
//go:embed *.sql
var Files embed.FS
