package trace

// func TestLocalClientWrite(t *testing.T) {
// 	// Setup
// 	logger := log.NewNopLogger()
// 	cfg := config.DefaultConfig()
// 	cfg.SetRoot(t.TempDir())
// 	cfg.Instrumentation.TraceDB = "test"
// 	cfg.Instrumentation.TraceBufferSize = 100

// 	// Test cases
// 	tests := []struct {
// 		name        string
// 		ev          Event
// 		table       string
// 		expectError bool
// 	}{
// 		{
// 			name:    "successfully writes event to file",
// 			setup:   func() {},
// 			chainID: "chain1",
// 			nodeID:  "node1",
// 			table:   "events",
// 		},
// 		{
// 			name: "fails to write event to non-existing table",
// 			setup: func() {
// 				// Here you might adjust setup to have no files open, or the table not included
// 			},
// 			chainID:     "chain1",
// 			nodeID:      "node1",
// 			table:       "nonExisting",
// 			expectError: true,
// 		},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {

// 		})
// 	}
// }
