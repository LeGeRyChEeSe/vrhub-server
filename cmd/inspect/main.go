// Command inspect is an offline read-only viewer for the games table of a
// vrhub-server SQLite database. It is a maintenance helper, not part of the
// server runtime.
//
// Usage:
//
//	inspect [path-to-vrhub.db]
//
// With no argument it falls back to the default data directory used by the
// server (Windows: %APPDATA%/vrhub-server, Unix: $HOME/.vrhub-server).
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite"
)

func defaultDBPath() string {
	var dataDir string
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		dataDir = filepath.Join(appData, "vrhub-server")
	} else {
		home := os.Getenv("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		dataDir = filepath.Join(home, ".vrhub-server")
	}
	return filepath.Join(dataDir, "vrhub.db")
}

func main() {
	dbPath := defaultDBPath()
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT release_name, package_name, hash, exposed, corrupted FROM games ORDER BY release_name`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-32s %-42s %-34s %-8s %-10s\n", "release_name", "package_name", "hash", "exposed", "corrupted")
	fmt.Println("-----------------------------------------------------------------------------------------------------------------------------")
	count := 0
	exposedCount := 0
	for rows.Next() {
		var rn, pn, h string
		var e, c bool
		if err := rows.Scan(&rn, &pn, &h, &e, &c); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			continue
		}
		fmt.Printf("%-32s %-42s %-34s %-8v %-10v\n", rn, pn, h, e, c)
		count++
		if e {
			exposedCount++
		}
	}
	fmt.Println()
	fmt.Printf("Total: %d games, %d exposed\n", count, exposedCount)
}
