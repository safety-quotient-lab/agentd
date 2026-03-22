// Package migrate applies the canonical schema to state.db at startup.
// The schema embeds into the binary via go:embed, eliminating external
// file dependencies. Filesystem fallback checks scripts/schema.sql for
// project-specific overrides.
package migrate

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/safety-quotient-lab/agentd/internal/db"
)

//go:embed schema.sql
var embeddedSchema string

// Run applies schema.sql to the database idempotently.
// The schema uses CREATE TABLE IF NOT EXISTS and INSERT OR IGNORE,
// so running it multiple times produces no harm.
func Run(database *db.DB, projectRoot string) error {
	// Prefer filesystem schema if present (project-specific overrides)
	var script string
	var source string
	if schemaPath := findSchema(projectRoot); schemaPath != "" {
		data, err := os.ReadFile(schemaPath)
		if err != nil {
			return fmt.Errorf("read schema.sql: %w", err)
		}
		script = string(data)
		source = schemaPath
	} else {
		script = embeddedSchema
		source = "embedded"
	}

	slog.Info("applying schema", "component", "migrate", "source", source, "bytes", len(script))

	// Schema.sql contains both CREATE TABLE IF NOT EXISTS (idempotent) and
	// ALTER TABLE ADD COLUMN (fails if column already exists). Split the
	// script into individual statements and execute each, ignoring
	// "duplicate column" errors from ALTER TABLE on subsequent runs.
	if err := applyStatements(database, script); err != nil {
		return fmt.Errorf("apply schema.sql: %w", err)
	}

	slog.Info("schema applied successfully", "component", "migrate")
	return nil
}

// applyStatements splits a SQL script into individual statements and
// executes each one. ALTER TABLE errors for duplicate columns get
// logged as warnings rather than treated as fatal — these occur
// normally on subsequent runs when migrations have already applied.
func applyStatements(database *db.DB, script string) error {
	statements := splitStatements(script)
	applied, skipped := 0, 0
	for _, stmt := range statements {
		if stmt == "" {
			continue
		}
		_, err := database.Exec(stmt)
		if err != nil {
			// SQLite (modernc.org/sqlite) does not expose typed errors for
			// schema-already-exists conditions. String matching serves as
			// last resort — wrapped in a named predicate for clarity.
			if isSQLiteAlreadyMigratedError(err) {
				skipped++
				continue
			}
			return fmt.Errorf("statement failed: %w\n  SQL: %.100s", err, stmt)
		}
		applied++
	}
	slog.Info("migration statements executed",
		"component", "migrate", "applied", applied, "skipped", skipped)
	return nil
}

// splitStatements splits a SQL script on semicolons, handling:
// - Single-line comments (-- to end of line)
// - String literals ('...' containing semicolons)
// - CREATE TRIGGER blocks (semicolons inside BEGIN...END)
func splitStatements(script string) []string {
	var statements []string
	var current []byte
	inString := false
	inTrigger := false

	for i := 0; i < len(script); i++ {
		ch := script[i]

		// Skip single-line comments (-- to end of line)
		if !inString && ch == '-' && i+1 < len(script) && script[i+1] == '-' {
			for i < len(script) && script[i] != '\n' {
				i++
			}
			current = append(current, '\n')
			continue
		}

		// Track string literals
		if ch == '\'' {
			inString = !inString
		}

		// Track BEGIN...END blocks (triggers)
		if !inString {
			rest := strings.ToUpper(strings.TrimSpace(string(current)))
			if strings.HasSuffix(rest, "BEGIN") {
				inTrigger = true
			}
			// END followed by semicolon closes the trigger
			if inTrigger && ch == ';' {
				trimmed := strings.TrimSpace(string(current))
				if strings.HasSuffix(strings.ToUpper(trimmed), "END") {
					current = append(current, ch)
					stmt := strings.TrimSpace(string(current))
					if stmt != "" {
						statements = append(statements, stmt)
					}
					current = current[:0]
					inTrigger = false
					continue
				}
			}
		}

		// Split on semicolons outside strings and outside triggers
		if ch == ';' && !inString && !inTrigger {
			stmt := strings.TrimSpace(string(current))
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current = current[:0]
			continue
		}

		current = append(current, ch)
	}
	if stmt := strings.TrimSpace(string(current)); stmt != "" {
		statements = append(statements, stmt)
	}
	return statements
}

// isSQLiteAlreadyMigratedError detects errors from ALTER TABLE ADD COLUMN
// or CREATE TABLE when the schema element already exists. The modernc.org/sqlite
// driver does not expose typed errors for these conditions, so string matching
// against the error message serves as the last resort.
func isSQLiteAlreadyMigratedError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") ||
		strings.Contains(msg, "already exists")
}

// findSchema locates schema.sql in the project directory tree.
// Checks (in order): scripts/schema.sql, platform/shared/scripts/schema.sql
func findSchema(projectRoot string) string {
	candidates := []string{
		filepath.Join(projectRoot, "scripts", "schema.sql"),
		filepath.Join(projectRoot, "platform", "shared", "scripts", "schema.sql"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
