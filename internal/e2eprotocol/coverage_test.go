package e2eprotocol

import (
	"strings"
	"testing"
)

func TestDecodeFailures(t *testing.T) {
	for _, data := range []string{"", "{", "{}", `{"event":"error","error":"failed"}`, strings.Repeat("x", 1024*1024)} {
		if records, err := Decode([]byte(data)); err == nil || records != nil {
			t.Fatalf("accepted invalid stream of %d bytes", len(data))
		}
	}
	records, err := Decode([]byte("{\"event\":\"ready\"}\n{\"event\":\"started\"}\n"))
	if err != nil || len(records) != 2 || records[0].Event != "ready" || records[1].Event != "started" {
		t.Fatalf("records=%v err=%v", records, err)
	}
}
