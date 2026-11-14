package e2eprotocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Decode accepts nonempty JSON-line streams with an event in every record.
func Decode(data []byte) ([]Record, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var records []Record
	for scanner.Scan() {
		var r Record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("invalid fixture JSON: %w", err)
		}
		if r.Event == "" {
			return nil, errors.New("missing fixture event")
		}
		if r.Event == "error" {
			return nil, fmt.Errorf("fixture error: %s", r.Error)
		}
		records = append(records, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan fixture JSON: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("fixture emitted no records")
	}
	return records, nil
}
