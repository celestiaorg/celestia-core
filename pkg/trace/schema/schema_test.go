package schema

import (
	"fmt"
	"strings"
	"testing"
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

func TestAllTables(t *testing.T) {
	fmt.Println(strings.Join(AllTables(), ","))
}
