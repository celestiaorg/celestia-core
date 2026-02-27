package client

import (
	"fmt"
	"io"
	"net/http"
	"time"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// DebugHalt calls the unsafe_debug_halt RPC endpoint to halt consensus.
// This pauses block commits while allowing message processing to continue,
// which forces the node into catchup mode when unlocked.
//
// The endpoint parameter should be the RPC endpoint URL (e.g., "http://localhost:26657").
// Returns the height at which the halt was initiated.
func DebugHalt(endpoint string) (*ctypes.ResultUnsafeDebugHalt, error) {
	url := fmt.Sprintf("%s/unsafe_debug_halt", endpoint)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to call unsafe_debug_halt: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsafe_debug_halt returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON-RPC response
	var rpcResp struct {
		Result *ctypes.ResultUnsafeDebugHalt `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}

	if err := cmtjson.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s - %s", rpcResp.Error.Code, rpcResp.Error.Message, rpcResp.Error.Data)
	}

	return rpcResp.Result, nil
}

// DebugUnlock calls the unsafe_debug_unlock RPC endpoint to unlock consensus.
// This resumes block commits after a debug halt, allowing the node to catch up.
//
// The endpoint parameter should be the RPC endpoint URL (e.g., "http://localhost:26657").
// Returns information about the halt duration and heights.
func DebugUnlock(endpoint string) (*ctypes.ResultUnsafeDebugUnlock, error) {
	url := fmt.Sprintf("%s/unsafe_debug_unlock", endpoint)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to call unsafe_debug_unlock: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unsafe_debug_unlock returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse JSON-RPC response
	var rpcResp struct {
		Result *ctypes.ResultUnsafeDebugUnlock `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}

	if err := cmtjson.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s - %s", rpcResp.Error.Code, rpcResp.Error.Message, rpcResp.Error.Data)
	}

	return rpcResp.Result, nil
}

// DebugHaltAndPrint calls DebugHalt and prints the result to stdout.
func DebugHaltAndPrint(endpoint string) error {
	result, err := DebugHalt(endpoint)
	if err != nil {
		return err
	}

	fmt.Printf("Debug Halt Result:\n")
	fmt.Printf("  Height: %d\n", result.Height)
	fmt.Printf("  Halted: %v\n", result.Halted)
	return nil
}

// DebugUnlockAndPrint calls DebugUnlock and prints the result to stdout.
func DebugUnlockAndPrint(endpoint string) error {
	result, err := DebugUnlock(endpoint)
	if err != nil {
		return err
	}

	fmt.Printf("Debug Unlock Result:\n")
	fmt.Printf("  Was Halted: %v\n", result.WasHalted)
	if result.WasHalted {
		fmt.Printf("  Halt Height: %d\n", result.HaltHeight)
		fmt.Printf("  Current Height: %d\n", result.CurrentHeight)
		fmt.Printf("  Halt Duration: %v\n", time.Duration(result.HaltDurationNs))
	}
	return nil
}
