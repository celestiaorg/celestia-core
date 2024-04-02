package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Define a test struct with various field types and json tags
type TestStruct struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
}

// Mock for a custom type with String method
type CustomType int

// TestStructWithCustomType includes a field with a custom type having a String method
type TestStructWithCustomType struct {
	ID   int        `json:"id"`
	Type CustomType `json:"type"`
}

// TestToMap tests the toMap function with a simple struct
func TestToMap(t *testing.T) {
	// Create a test case with the struct and expected map
	tests := []struct {
		name     string
		input    interface{}
		expected map[string]interface{}
	}{
		{
			name: "Simple struct",
			input: TestStruct{
				Name:  "John Doe",
				Age:   30,
				Email: "john@example.com",
			},
			expected: map[string]interface{}{
				"name":  "John Doe",
				"age":   30,
				"email": "john@example.com",
			},
		},
		{
			name: "Struct with custom type",
			input: TestStructWithCustomType{
				ID:   1,
				Type: CustomType(5),
			},
			expected: map[string]interface{}{
				"id":   1,
				"type": CustomType(5),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := toMap(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, result)
		})
	}
}
