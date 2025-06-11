package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cometbft/cometbft/types"
)

// SaveBlockToFile saves a CometBFT block as JSON to a file in the given directory.
func SaveBlockToFile(dir string, filename string, block *types.Block) error {
	// Ensure the directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Build full file path
	filePath := filepath.Join(dir, filename)

	// Open file for writing (create if not exists, truncate if exists)
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Marshal block to JSON (you can use json.Indent if you want pretty output)
	blockJSON, err := json.MarshalIndent(block, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal block to JSON: %w", err)
	}

	// Write JSON to file
	if _, err := file.Write(blockJSON); err != nil {
		return fmt.Errorf("failed to write JSON to file: %w", err)
	}

	return nil
}
