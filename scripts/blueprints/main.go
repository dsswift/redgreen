// Command blueprints prints the blueprints redgreen offers, as JSON, for
// scripts/check-expressions.mts.
package main

import (
	"encoding/json"
	"os"

	"github.com/dsswift/redgreen/internal/catalog"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(catalog.Blueprints()); err != nil {
		os.Exit(1)
	}
}
