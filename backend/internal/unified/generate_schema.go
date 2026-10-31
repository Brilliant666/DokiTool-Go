//go:build ignore

// Copy the one canonical schema for embedding; do not edit the generated copy.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Brilliant666/DokiTool-Go/backend/internal/unified"
)

func main() {
	canonical := filepath.Join("..", "..", "..", "docs", "schema", "unified-v1", "unified-v1.schema.json")
	data, err := os.ReadFile(canonical)
	if err != nil {
		panic(err)
	}
	if !json.Valid(data) {
		panic("canonical unified-v1 schema is invalid JSON")
	}
	schema, err := unified.Decode(data)
	if err != nil {
		panic(err)
	}
	if err := unified.ValidateSchemaDefinition(schema); err != nil {
		panic(err)
	}
	if err := os.WriteFile("unified-v1.generated.schema.json", data, 0644); err != nil {
		panic(err)
	}
	fmt.Println("embedded schema regenerated from", canonical)
}
