package sqlite

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func (d *Database) Snapshot(ctx context.Context) ([]byte, error) {
	if err := d.withContext(ctx); err != nil {
		return nil, err
	}

	tables, err := snapshotTables(d.db)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString("-- Spot Wu Family catalog snapshot\n")
	buf.WriteString("-- Generated deterministically from data/catalog.db\n")
	buf.WriteString("PRAGMA foreign_keys=OFF;\n")
	buf.WriteString("BEGIN TRANSACTION;\n")
	for _, table := range tables {
		if err := appendTableSnapshot(&buf, d.db, table); err != nil {
			return nil, err
		}
	}
	buf.WriteString("COMMIT;\n")
	buf.WriteString("PRAGMA foreign_keys=ON;\n")

	return buf.Bytes(), nil
}

func (d *Database) WriteSnapshot(ctx context.Context, path string) error {
	snapshot, err := d.Snapshot(ctx)
	if err != nil {
		return err
	}

	current, err := ReadSnapshot(path)
	if err == nil && bytes.Equal(current, snapshot) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing snapshot: %w", err)
	}
	if err := writeSnapshot(path, snapshot); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	return nil
}

func ReadSnapshot(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return data, nil
	}

	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open gzip snapshot: %w", err)
	}
	defer func() { _ = reader.Close() }()

	uncompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read gzip snapshot: %w", err)
	}

	return uncompressed, nil
}

func writeSnapshot(path string, snapshot []byte) error {
	if !strings.HasSuffix(path, ".gz") {
		return os.WriteFile(path, snapshot, 0o644)
	}

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(snapshot); err != nil {
		_ = writer.Close()
		return fmt.Errorf("compress snapshot: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish compressed snapshot: %w", err)
	}

	return os.WriteFile(path, compressed.Bytes(), 0o644)
}

func RestoreSnapshot(ctx context.Context, db *sql.DB, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	data, err := ReadSnapshot(path)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(data)); err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}

	return nil
}

func snapshotTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list snapshot tables: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("scan snapshot table: %w", err)
		}
		if isTransientSnapshotTable(table) {
			continue
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshot tables: %w", err)
	}

	return tables, nil
}

func isTransientSnapshotTable(table string) bool {
	switch table {
	case "artist_metadata_refreshes":
		return true
	default:
		return false
	}
}

func appendTableSnapshot(buf *bytes.Buffer, db *sql.DB, table string) error {
	columns, err := tableColumns(db, table)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}

	buf.WriteString("DELETE FROM ")
	buf.WriteString(quoteIdentifier(table))
	buf.WriteString(";\n")

	query := fmt.Sprintf(
		"SELECT %s FROM %s ORDER BY %s",
		joinIdentifiers(columns),
		quoteIdentifier(table),
		joinIdentifiers(columns),
	)
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("snapshot table %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	values := make([]any, len(columns))
	valuePointers := make([]any, len(columns))
	for i := range values {
		valuePointers[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(valuePointers...); err != nil {
			return fmt.Errorf("scan snapshot row from %s: %w", table, err)
		}
		buf.WriteString("INSERT INTO ")
		buf.WriteString(quoteIdentifier(table))
		buf.WriteString(" (")
		buf.WriteString(joinIdentifiers(columns))
		buf.WriteString(") VALUES (")
		for i, value := range values {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(sqlLiteral(value))
		}
		buf.WriteString(");\n")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate snapshot rows from %s: %w", table, err)
	}

	return nil
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + quoteIdentifier(table) + ")")
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan column for %s: %w", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns for %s: %w", table, err)
	}
	sort.Strings(columns)

	return columns, nil
}

func joinIdentifiers(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteIdentifier(value))
	}

	return strings.Join(quoted, ", ")
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqlLiteral(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case []byte:
		return quoteString(string(v))
	case string:
		return quoteString(v)
	default:
		return quoteString(fmt.Sprint(v))
	}
}

func quoteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
