package schema

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/cometbft/cometbft/config"
)

func init() {
	config.DefaultTracingTables = strings.Join(AllTables(), ",")
}

func AllTables() []string {
	tables := []string{}
	tables = append(tables, MempoolTables()...)
	tables = append(tables, ConsensusTables()...)
	return tables
}

type TransferType int

const (
	Download TransferType = iota
	Upload
)

func (t TransferType) String() string {
	switch t {
	case Download:
		return "download"
	case Upload:
		return "upload"
	default:
		return "unknown"
	}
}

// toMap takes a struct and returns a map representation using the struct's JSON tags as keys.
func toMap(s interface{}) (map[string]interface{}, error) {
	// Ensure the input is a struct
	val := reflect.ValueOf(s)
	if val.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input is not a struct")
	}

	result := make(map[string]interface{})
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Get the json tag for the field
		jsonTag := fieldType.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			// Skip fields without a json tag or explicitly ignored
			continue
		}

		// Check if the field is a type with a String() method (like TransferType)
		if field.Type().Kind() == reflect.Struct || field.Type().Kind() == reflect.Ptr {
			// Ensure there is a String method
			if method := field.MethodByName("String"); method.IsValid() {
				result[jsonTag] = method.Call(nil)[0].Interface()
				continue
			}
		}

		// Use the json tag as key and field value as map value
		result[jsonTag] = field.Interface()
	}

	return result, nil
}
