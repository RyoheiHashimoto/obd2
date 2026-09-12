package obd2

import (
	"slices"
	"testing"
)

func TestDTCString(t *testing.T) {
	tests := []struct {
		d    DTC
		want string
	}{
		{0x0133, "P0133"},
		{0x1234, "P1234"},
		{0x3456, "P3456"},
		{0x4123, "C0123"},
		{0x8123, "B0123"},
		{0xC100, "U0100"},
		{0xFFFF, "U3FFF"},
	}
	for _, tt := range tests {
		if got := tt.d.String(); got != tt.want {
			t.Errorf("DTC(%#04x) = %s, want %s", uint16(tt.d), got, tt.want)
		}
		back, err := ParseDTC(tt.want)
		if err != nil || back != tt.d {
			t.Errorf("ParseDTC(%s) = %#04x, %v", tt.want, uint16(back), err)
		}
	}
}

func TestParseDTCRejects(t *testing.T) {
	for _, s := range []string{"", "P01", "P01330", "X0100", "P4000", "P01G0"} {
		if _, err := ParseDTC(s); err == nil {
			t.Errorf("ParseDTC(%q) succeeded", s)
		}
	}
}

func TestParseDTCs(t *testing.T) {
	tests := []struct {
		name string
		r    Response
		want []DTC
	}{
		{"CAN, two codes", Response{Protocol: ProtocolCAN11, Data: []byte{0x43, 0x02, 0x01, 0x33, 0xC1, 0x00}}, []DTC{0x0133, 0xC100}},
		{"CAN, none", Response{Protocol: ProtocolCAN11, Data: []byte{0x43, 0x00}}, []DTC{}},
		{"CAN, padded frame", Response{Protocol: ProtocolCAN29, Data: []byte{0x43, 0x01, 0x03, 0x00, 0x00, 0x00}}, []DTC{0x0300}},
		{"ISO 9141, one code and filler", Response{Protocol: ProtocolISO9141, Data: []byte{0x43, 0x01, 0x33, 0x00, 0x00, 0x00, 0x00}}, []DTC{0x0133}},
		{"J1850, three codes", Response{Protocol: ProtocolJ1850VPW, Data: []byte{0x43, 0x01, 0x33, 0x03, 0x01, 0x43, 0x20}}, []DTC{0x0133, 0x0301, 0x4320}},
	}
	for _, tt := range tests {
		got, err := ParseDTCs(tt.r)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, %v; want %v", tt.name, got, err, tt.want)
		}
	}

	if _, err := ParseDTCs(Response{Protocol: ProtocolCAN11, Data: []byte{0x43, 0x02, 0x01, 0x33}}); err == nil {
		t.Error("truncated CAN response parsed without error")
	}
}
