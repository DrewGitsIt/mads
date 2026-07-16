package main

import (
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/drewkidwell/mads/services/data/internal/store"
)

// checkSchemaCompatible refuses to proceed when the on-disk schema is newer
// than what this binary's embedded migration set knows about (a newer mads
// build ran migrations this older binary has never seen). This is a boot-
// time refusal, not an automatic rollback: store.Rollback's down.sql files
// are inherently destructive (several DROP COLUMN/DROP TABLE), so silently
// invoking them the moment an older binary notices a newer schema would
// discard whatever the newer version's migrations added, with no operator
// confirmation. The safe default is to refuse to start and say why;
// store.Rollback remains available as an explicit, human-invoked recovery
// primitive for whoever operates an actual downgrade.
func checkSchemaCompatible(db *sql.DB, fsys fs.FS) error {
	version, err := store.Version(db)
	if err != nil {
		return err
	}
	maxKnown, err := store.MaxVersion(fsys)
	if err != nil {
		return err
	}
	if version > maxKnown {
		return fmt.Errorf("data: on-disk schema is v%d but this binary only knows up to v%d — "+
			"this binary is older than the data it's pointed at; install a newer mads build "+
			"before starting the Data Service against this data directory", version, maxKnown)
	}
	return nil
}
