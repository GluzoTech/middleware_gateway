package migrations_test

import (
	"regexp"
	"testing"

	"github.com/gluzo/integration-gateway/app/database/migrations"
)

var versionPattern = regexp.MustCompile(`^\d{4}_[a-z0-9_]+$`)

// TestEmbeddedFilesAreWellFormed guards the migrations shipped with the
// binary: every file must load, follow the NNNN_description naming scheme
// and carry a unique version.
func TestEmbeddedFilesAreWellFormed(t *testing.T) {
	migs, err := migrations.Load(migrations.Files())
	if err != nil {
		t.Fatalf("Load embedded migrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations found")
	}
	seen := make(map[string]bool, len(migs))
	for _, m := range migs {
		if !versionPattern.MatchString(m.Version) {
			t.Errorf("migration %q does not match %s", m.Version, versionPattern)
		}
		prefix := m.Version[:4]
		if seen[prefix] {
			t.Errorf("duplicate migration number %s", prefix)
		}
		seen[prefix] = true
	}
}
