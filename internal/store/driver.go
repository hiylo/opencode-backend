package store

import (
	"context"
	"fmt"
	"net/url"

	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" database/sql driver
	_ "modernc.org/sqlite"              // register "sqlite" database/sql driver
)

// DriverName maps a logical driver name to the registered database/sql driver.
func DriverName(driver string) (string, error) {
	switch driver {
	case "sqlite":
		return "sqlite", nil
	case "postgres":
		return "pgx", nil
	default:
		return "", fmt.Errorf("unsupported driver %q", driver)
	}
}

// OpenFromConfig opens a store using the logical driver name and its DSN.
// driver is "sqlite" or "postgres"; dsn is a file path or a connection string.
func OpenFromConfig(ctx context.Context, driver, dsn string) (Store, error) {
	reg, err := DriverName(driver)
	if err != nil {
		return nil, err
	}
	return Open(ctx, reg, dsn)
}

// SQLiteDSN builds a SQLite DSN from a file path, enabling WAL journal mode.
func SQLiteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_pragma", "journal_mode(WAL)")
	q.Set("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}
