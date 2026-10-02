package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	marker, err := os.Create(os.Getenv("PREFLIGHT_LINT_MARKER"))
	if err != nil {
		panic(err)
	}
	defer marker.Close()
	if err := json.NewEncoder(marker).Encode(map[string]any{"args": os.Args[1:], "dir": dir}); err != nil {
		panic(err)
	}
	fmt.Println("synthetic checkout lint success")
}
