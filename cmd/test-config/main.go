package main

import (
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
)

type C struct {
	GameFolders []string `toml:"game_folders"`
}

func main() {
	f, _ := os.CreateTemp("", "test*.toml")
	f.WriteString(`game_folders = ["a", "b"]\n`)
	f.Close()

	var c C
	_, err := toml.DecodeFile(f.Name(), &c)
	fmt.Println("err:", err)
	fmt.Printf("folders: %v\n", c.GameFolders)
}
