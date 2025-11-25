package main

import (
	"fmt"
	"log" // Use standard logging package
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/snikch/goodman/hooks"
	"github.com/snikch/goodman/transaction"
)

// Define the address for the hook server to listen on
const hookServerAddress = "127.0.0.1:8080" // Use a defined address/port

func main() {
	// Initialize the hooks manager
	h := hooks.NewHooks()
	
	// Initialize the hook server runner
	server := hooks.NewServer(hooks.NewHooksRunner(h))

	// --- 1. Global Setup Hook ---
	h.BeforeAll(func(t []*transaction.Transaction) {
		// Log the name of the first transaction to confirm hook execution
		log.Printf("INFO: Starting Dredd hook execution for API document: %s", t[0].Name)
	})

	// --- 2. Per-Transaction Hook (Skipping Logic) ---
	h.BeforeEach(func(t *transaction.Transaction) {
		// Dredd requires the hook server to run beforehand.
		// The following logic skips transactions that require specific data 
		// (e.g., hashes from prior transactions, or complex nested payloads) 
		// that are not provided in the static API blueprint/OpenAPI document.
		
		if strings.HasPrefix(t.Name, "Tx") ||
			strings.HasPrefix(t.Name, "Info > /broadcast_evidence") || // Requires proper evidence example
			strings.HasPrefix(t.Name, "ABCI > /abci_query") ||       // Requires proper path and data examples
			strings.HasPrefix(t.Name, "Info > /tx") {                 // Requires a pre-existing transaction hash
			
			t.Skip = true
			log.Printf("SKIPPED: %s (Missing dynamic data/complex setup)", t.Name)
		}
	})

	// --- 3. Server Initialization and Graceful Shutdown ---
	
	// Create a channel to listen for OS signals (e.g., Ctrl+C)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Set the server listener to a specific address
	if err := server.Listen(hookServerAddress); err != nil {
		log.Fatalf("FATAL: Failed to listen on %s: %v", hookServerAddress, err)
	}

	// Start the server in a goroutine so it doesn't block the main thread
	go func() {
		log.Printf("INFO: Dredd Hook Server listening on %s", hookServerAddress)
		if err := server.Serve(); err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("FATAL: Server failed unexpectedly: %v", err)
		}
	}()

	// Wait until an OS signal is received
	<-quit

	// Execute teardown logic (closing the listener)
	log.Println("INFO: Shutting down Dredd Hook Server...")
	if err := server.Listener.Close(); err != nil {
		log.Printf("WARNING: Error closing listener: %v", err)
	}
	
	// The final 'fmt.Print("FINE")' is removed as it's unreliable and non-standard.
	log.Println("INFO: Hook Server shutdown complete.")
}
