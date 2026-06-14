package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	_ "modernc.org/sqlite"
)

func main() {
	dataDir := os.Getenv("APPDATA") + "\\vrhub-server"
	dbPath := filepath.Join(dataDir, "vrhub.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT package_name, hash, game_name FROM games ORDER BY package_name`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	defer rows.Close()

	type gameRow struct {
		PackageName string
		Hash        string
		GameName    string
	}

	var games []gameRow
	for rows.Next() {
		var g gameRow
		if err := rows.Scan(&g.PackageName, &g.Hash, &g.GameName); err != nil {
			continue
		}
		games = append(games, g)
	}

	updated := 0
	iconsExtracted := 0
	for _, g := range games {
		// Cherche l'APK dans le dossier du jeu
		gameDir := filepath.Join(dataDir, "games", g.Hash, g.PackageName)
		entries, err := os.ReadDir(gameDir)
		if err != nil {
			fmt.Printf("SKIP %-40s (pas de dossier)\n", g.PackageName)
			continue
		}

		var apkPath string
		for _, e := range entries {
			if strings.EqualFold(filepath.Ext(e.Name()), ".apk") {
				apkPath = filepath.Join(gameDir, e.Name())
				break
			}
		}
		if apkPath == "" {
			fmt.Printf("SKIP %-40s (pas d'APK)\n", g.PackageName)
			continue
		}

		// Fix name if empty or equal to package name
		needsNameFix := g.GameName == "" || g.GameName == g.PackageName
		if needsNameFix {
			meta, err := game.ExtractAPKMetadata(apkPath)
			if err != nil {
				fmt.Printf("SKIP %-40s (erreur extraction: %v)\n", g.PackageName, err)
				continue
			}

			newName := meta.Label
			if newName == "" {
				newName = g.PackageName
			}

			_, err = db.Exec("UPDATE games SET game_name = ? WHERE package_name = ?", newName, g.PackageName)
			if err != nil {
				fmt.Printf("ERR  %-40s (update: %v)\n", g.PackageName, err)
				continue
			}

			fmt.Printf("OK   %-40s → %s\n", g.PackageName, newName)
			updated++
		}

		// Extract icon if missing
		iconDir := filepath.Join(dataDir, "metadata", "icons")
		iconPath := filepath.Join(iconDir, g.PackageName+".png")
		if _, err := os.Stat(iconPath); os.IsNotExist(err) {
			if iconErr := game.ExtractAPKIcon(apkPath, iconPath); iconErr != nil {
				fmt.Printf("     icon extraction failed: %v\n", iconErr)
			} else {
				fmt.Printf("     icon extracted → %s\n", iconPath)
				iconsExtracted++
			}
		}
	}

	fmt.Printf("\n%d jeux mis à jour, %d icônes extraites\n", updated, iconsExtracted)
}
