package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime configuration for opencode-backend.
type Config struct {
	// ListenAddr is the address the HTTP server binds to, e.g. ":8080".
	ListenAddr string
	// OpenCodeURL is the base URL of the local OpenCode server, e.g. "http://127.0.0.1:4096".
	OpenCodeURL string
	// DBDriver is "sqlite" or "postgres".
	DBDriver string
	// SQLitePath is the database file path when DBDriver is sqlite.
	SQLitePath string
	// PostgresDSN is the connection string when DBDriver is postgres.
	PostgresDSN string
	// DefaultAdminPassword is the initial web admin password if no stored one exists.
	DefaultAdminPassword string
}

// Parse reads configuration from command-line flags and environment variables.
// Environment variables take precedence over flag defaults where set.
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("opencode-backend", flag.ContinueOnError)

	listenAddr := fs.String("listen", envOr("OCB_LISTEN", ":8080"), "HTTP listen address")
	opencodeURL := fs.String("opencode-url", envOr("OCB_OPENCODE_URL", "http://127.0.0.1:4096"), "local OpenCode server base URL")
	dbDriver := fs.String("db", envOr("OCB_DB", "sqlite"), "database driver: sqlite or postgres")
	sqlitePath := fs.String("sqlite-path", envOr("OCB_SQLITE_PATH", "opencode-backend.db"), "SQLite database file path")
	postgresDSN := fs.String("pg-dsn", os.Getenv("OCB_PG_DSN"), "PostgreSQL connection string")
	defaultAdmin := fs.String("default-admin-password", envOr("OCB_ADMIN_PASSWORD", "admin"), "default web admin password (used only on first initialization)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	driver := strings.ToLower(*dbDriver)
	if driver != "sqlite" && driver != "postgres" {
		return nil, fmt.Errorf("invalid db driver %q: must be sqlite or postgres", *dbDriver)
	}
	if driver == "postgres" && strings.TrimSpace(*postgresDSN) == "" {
		return nil, fmt.Errorf("db=postgres requires a postgres DSN (--pg-dsn or OCB_PG_DSN)")
	}

	return &Config{
		ListenAddr:           *listenAddr,
		OpenCodeURL:          strings.TrimRight(*opencodeURL, "/"),
		DBDriver:             driver,
		SQLitePath:           *sqlitePath,
		PostgresDSN:          *postgresDSN,
		DefaultAdminPassword: *defaultAdmin,
	}, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// Port returns the numeric port from ListenAddr (e.g. ":8080" -> 8080).
func (c *Config) Port() int {
	idx := strings.LastIndex(c.ListenAddr, ":")
	if idx < 0 || idx == len(c.ListenAddr)-1 {
		return 0
	}
	n, err := strconv.Atoi(c.ListenAddr[idx+1:])
	if err != nil {
		return 0
	}
	return n
}
