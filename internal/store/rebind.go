package store

import "strings"

// rebind converts a SQLite-style statement (using ? placeholders) into a
// statement using $N placeholders for PostgreSQL. SQLite queries are returned
// unchanged. This keeps one source of truth for SQL while supporting both
// drivers through database/sql.
func rebind(driver, query string) string {
	if driver != "pgx" {
		return query
	}
	// Replace each ? with $N. This is a simple scan; the codebase never uses
	// '?' inside string literals, so a naive scan is safe here.
	var sb strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			sb.WriteString("$")
			sb.WriteString(itoa(n))
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [16]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
