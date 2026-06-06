package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "github.com/mattn/go-sqlite3"
)

// Store is the Hub's durable storage layer.
// Everything involving money, tickets, or settlement goes through here.
// The ledger tables (invoices, tickets, usage_reports, settlement_entries,
// payouts) are append-only — enforced by database triggers, not just convention.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at the given path.
// If path is empty, it uses the platform-specific default data directory.
// Creates the directory structure and runs migrations on first open.
func Open(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = defaultDBPath()
		if err != nil {
			return nil, fmt.Errorf("determine default db path: %w", err)
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dir, err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Verify the connection works.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return s, nil
}

// Close shuts down the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying *sql.DB for transactions that span multiple
// repository operations. Use sparingly — prefer the typed methods.
func (s *Store) DB() *sql.DB {
	return s.db
}

// defaultDBPath returns the platform-specific database file path.
//
//	Linux:   ~/.local/share/arfl/hub.db
//	macOS:   ~/Library/Application Support/arfl/hub.db
//	Windows: %APPDATA%\arfl\hub.db
func defaultDBPath() (string, error) {
	var base string

	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Application Support", "arfl")
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return "", fmt.Errorf("APPDATA not set")
		}
		base = filepath.Join(appdata, "arfl")
	default: // Linux and others
		dataDir := os.Getenv("XDG_DATA_HOME")
		if dataDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dataDir = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(dataDir, "arfl")
	}

	return filepath.Join(base, "hub.db"), nil
}
