package obd2

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDTCJSON(t *testing.T) {
	b, err := json.Marshal([]DTC{0x0133, 0xC100})
	if err != nil || string(b) != `["P0133","U0100"]` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var back []DTC
	if err := json.Unmarshal(b, &back); err != nil || !slices.Equal(back, []DTC{0x0133, 0xC100}) {
		t.Errorf("Unmarshal = %v, %v", back, err)
	}
	if err := json.Unmarshal([]byte(`["X9999"]`), &back); err == nil {
		t.Error("an invalid code was accepted")
	}
}
