// Package dbschema holds the bootstrap migration that creates module schemas and roles.
package dbschema

import (
	"embed"
	"io/fs"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var files embed.FS

// Source returns the bootstrap migration source. It must run before any module source.
func Source() migrate.Source {
	sub, err := fs.Sub(files, "migrations")
	if err != nil {
		panic(err)
	}
	return migrate.Source{Module: "bootstrap", Schema: "platform", FS: sub}
}
