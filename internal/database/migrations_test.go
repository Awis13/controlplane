package database

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// There is no database in the test environment, so these checks work on the
// embedded files themselves. They catch the mistakes that would otherwise only
// show up when a deployment runs the migrations: a missing pair, a gap or a
// repeat in the version sequence, or a file golang-migrate cannot parse.

var migrationName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

func migrationFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		t.Fatal("no migrations were embedded")
	}
	return names
}

func TestMigrations_NamesAreWellFormed(t *testing.T) {
	for _, name := range migrationFiles(t) {
		if !migrationName.MatchString(name) {
			t.Errorf("%q does not match golang-migrate's <version>_<name>.<up|down>.sql", name)
		}
	}
}

func TestMigrations_EveryUpHasADown(t *testing.T) {
	directions := map[string]map[string]bool{}

	for _, name := range migrationFiles(t) {
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			continue // reported by TestMigrations_NamesAreWellFormed
		}
		key := m[1] + "_" + m[2]
		if directions[key] == nil {
			directions[key] = map[string]bool{}
		}
		directions[key][m[3]] = true
	}

	for key, dirs := range directions {
		if !dirs["up"] {
			t.Errorf("%s has a down migration but no up", key)
		}
		if !dirs["down"] {
			t.Errorf("%s has an up migration but no down", key)
		}
	}
}

// TestMigrations_VersionsAreContiguous guards the sequence: golang-migrate
// tolerates gaps, but a repeated version means two different changes claim the
// same slot and only one of them will ever run.
func TestMigrations_VersionsAreContiguous(t *testing.T) {
	seen := map[int]string{}

	for _, name := range migrationFiles(t) {
		m := migrationName.FindStringSubmatch(name)
		if m == nil || m[3] != "up" {
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("version in %q: %v", name, err)
		}
		if prev, ok := seen[version]; ok {
			t.Errorf("version %d is claimed by both %q and %q", version, prev, name)
		}
		seen[version] = name
	}

	for i := 1; i <= len(seen); i++ {
		if _, ok := seen[i]; !ok {
			t.Errorf("version %d is missing, so the sequence has a gap", i)
		}
	}
}

func TestMigrations_FilesAreNotEmpty(t *testing.T) {
	for _, name := range migrationFiles(t) {
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		if len(strings.TrimSpace(stripSQLComments(string(body)))) == 0 {
			t.Errorf("%q contains no statements", name)
		}
	}
}

func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		fmt.Fprintln(&b, line)
	}
	return b.String()
}
