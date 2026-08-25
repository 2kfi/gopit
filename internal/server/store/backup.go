package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
)

// Dump writes a full SQL text dump (schema + data, .dump-compatible) of the
// database at path to w, reading through the modernc driver so no sqlite3 CLI
// is needed. All reads run inside one sql.Tx, which pins a single connection
// and holds an actual read transaction — WAL readers see a stable snapshot,
// safe to run against a live server.
func Dump(dbPath string, w io.Writer) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	fmt.Fprintln(w, "PRAGMA foreign_keys=OFF;")
	fmt.Fprintln(w, "BEGIN TRANSACTION;")

	rows, err := tx.Query(`SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND type IN ('table','index','trigger') ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, name`)
	if err != nil {
		return err
	}
	var schema []string
	type tbl struct{ name string }
	var tables []tbl
	for rows.Next() {
		var t, name, sql string
		if err := rows.Scan(&t, &name, &sql); err != nil {
			rows.Close()
			return err
		}
		if strings.HasPrefix(name, "sqlite_") { // internal bookkeeping: rebuilt on restore
			continue
		}
		schema = append(schema, sql+";")
		if t == "table" {
			tables = append(tables, tbl{name})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range schema {
		fmt.Fprintln(w, s)
	}

	sqlVal := func(v any) string {
		switch x := v.(type) {
		case nil:
			return "NULL"
		case []byte:
			return "X'" + fmt.Sprintf("%x", x) + "'"
		case int64, float64:
			return fmt.Sprintf("%v", x)
		default:
			return "'" + strings.ReplaceAll(fmt.Sprintf("%v", x), "'", "''") + "'"
		}
	}
	for _, t := range tables {
		if strings.HasPrefix(t.name, "sqlite_") { // internal bookkeeping: rebuilt on restore
			continue
		}
		drows, err := tx.Query(`SELECT * FROM "` + strings.ReplaceAll(t.name, `"`, `""`) + `"`)
		if err != nil {
			return err
		}
		cols, err := drows.Columns()
		if err != nil {
			drows.Close()
			return err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for drows.Next() {
			if err := drows.Scan(ptrs...); err != nil {
				drows.Close()
				return err
			}
			parts := make([]string, len(vals))
			for i, v := range vals {
				parts[i] = sqlVal(v)
			}
			fmt.Fprintf(w, "INSERT INTO %s VALUES(%s);\n", quoteIdent(t.name), strings.Join(parts, ","))
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return err
		}
	}

	fmt.Fprintln(w, "COMMIT;")
	return tx.Commit()
}

// Restore loads a SQL dump from r into a fresh database at dbPath. The dump
// is applied to a temp file first so a broken dump never clobbers the live
// DB, then swapped in atomically. Stop the service before restoring: the
// running server holds the old DB open in WAL mode.
func Restore(dbPath string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	tmp := dbPath + ".restore"
	os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+tmp+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return err
	}
	_, execErr := db.Exec(string(data))
	closeErr := db.Close()
	if execErr != nil {
		os.Remove(tmp)
		return execErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	// A leftover -wal/-shm from the old DB would replay stale frames on top
	// of the restored file; the new DB is freshly created in rollback mode.
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
	return os.Rename(tmp, dbPath)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
