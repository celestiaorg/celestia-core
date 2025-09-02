package types

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// Go lacks a simple and safe way to see if something is a typed nil.
// See:
//   - https://dave.cheney.net/2017/08/09/typed-nils-in-go-2
//   - https://groups.google.com/forum/#!topic/golang-nuts/wnH302gBa4I/discussion
//   - https://github.com/golang/go/issues/21538
func isTypedNil(o interface{}) bool {
	rv := reflect.ValueOf(o)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// Returns true if it has zero length.
func isEmpty(o interface{}) bool {
	rv := reflect.ValueOf(o)
	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() == 0
	default:
		return false
	}
}

// SaveBlockToFile saves a CometBFT block to a file in the given directory.
func SaveBlockToFile(dir string, filename string, block *Block) error {
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
		return fmt.Errorf("failed to write block to file: %w", err)
	}

	return nil
}

// SaveInvalidProposalWithMetadata saves an invalid proposal with metadata to a JSON file for debugging
func SaveInvalidProposalWithMetadata(dir string, filename string, proposal *Proposal, metadata map[string]interface{}) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	filePath := filepath.Join(dir, filename)

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Create a structure that combines proposal data and metadata
	proposalData := struct {
		Metadata map[string]interface{} `json:"metadata"`
		Proposal *Proposal              `json:"proposal"`
	}{
		Metadata: metadata,
		Proposal: proposal,
	}

	// Marshal to pretty JSON for readability
	proposalBZ, err := json.MarshalIndent(proposalData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal proposal data to JSON: %w", err)
	}

	if _, err := file.Write(proposalBZ); err != nil {
		return fmt.Errorf("failed to write proposal data to file: %w", err)
	}

	return nil
}
