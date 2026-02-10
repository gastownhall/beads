package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// outputJSON outputs data as pretty-printed JSON to stdout.
func outputJSON(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
func outputJSONError(err error, code string) {
	errObj := map[string]string{"error": err.Error()}
	if code != "" {
		errObj["code"] = code
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
}
