package e2eprotocol

import (
	"encoding/json"
	"testing"
)

func FuzzDecode(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		records, err := Decode(data)
		if err != nil {
			return
		}
		for _, r := range records {
			if r.Event == "" || r.Event == "error" {
				t.Fatal("invalid event accepted")
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(encoded); err != nil {
				t.Fatalf("round trip: %v", err)
			}
		}
	})
}
