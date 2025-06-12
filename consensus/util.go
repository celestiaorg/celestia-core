package consensus

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cometbft/cometbft/types"
)

// saveBlockToFile saves a CometBFT block to a file in the given directory.
func saveBlockToFile(dir string, filename string, block *types.Block) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filePath := filepath.Join(dir, filename)

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	blockPB, err := block.ToProto()
	if err != nil {
		return fmt.Errorf("failed to convert block to protobuf: %w", err)
	}

	blockBZ, err := blockPB.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal block to bytes: %w", err)
	}

	if _, err := file.Write(blockBZ); err != nil {
		return fmt.Errorf("failed to write JSON to file: %w", err)
	}

	return nil
}
