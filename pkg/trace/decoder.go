package trace

import (
	"bufio"
	"encoding/json"
	"os"
)

// ScanDecodeFile reads a file and decodes it into a slice of events via
// scanning. The table parameter is used to determine the type of the events.
// The file should be a jsonl file. The generic here are passed to the event
// type.
func ScanDecodeFile[T any](file *os.File) ([]Event[T], error) {
	scanner := bufio.NewScanner(file)
	var out []Event[T]
	_, err := file.Seek(0, 0)
	if err != nil {
		return out, nil
	}
	for scanner.Scan() {
		var row Event[T]
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return out, err
		}
		out = append(out, row)
	}
	return out, scanner.Err()
}
