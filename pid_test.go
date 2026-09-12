package obd2

import (
	"math"
	"slices"
	"testing"
)

func TestReadingFloat(t *testing.T) {
	tests := []struct {
		pid  PID
		data []byte
		want float64
	}{
		{EngineRPM, []byte{0x1A, 0xF8}, 1726},
		{VehicleSpeed, []byte{0x32}, 50},
		{CoolantTemp, []byte{0x7B}, 83},
		{ThrottlePosition, []byte{0xFF}, 100},
		{TimingAdvance, []byte{0x80}, 0},
		{ShortTermFuelTrimBank1, []byte{0x80}, 0},
		{ShortTermFuelTrimBank1, []byte{0x00}, -100},
		{MAFAirFlowRate, []byte{0x01, 0x2C}, 3},
		{ControlModuleVoltage, []byte{0x36, 0xB0}, 14},
		{EvapVaporPressure, []byte{0xFF, 0xFC}, -1},
		{Odometer, []byte{0x00, 0x01, 0xE2, 0x40}, 12345.6},
		{EngineFuelRate, []byte{0x00, 0x64}, 5},
		{CatalystTempBank1Sensor1, []byte{0x0F, 0xA0}, 360},
		{FuelInjectionTiming, []byte{0x69, 0x00}, 0},
		{DriverDemandTorque, []byte{0x7D}, 0},
		{CommandedEquivalenceRatio, []byte{0x80, 0x00}, 1},
	}
	for _, tt := range tests {
		got, err := Reading{PID: tt.pid, Data: tt.data}.Float()
		if err != nil || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%v % X = %v, %v; want %v", tt.pid, tt.data, got, err, tt.want)
		}
	}
}

func TestReadingFloatRejects(t *testing.T) {
	for _, r := range []Reading{
		{PID: MonitorStatus, Data: []byte{0, 0, 0, 0}}, // bit field
		{PID: 0xFE, Data: []byte{1}},                   // unknown PID
		{PID: EngineRPM, Data: []byte{0x1A}},           // too short
	} {
		if v, err := r.Float(); err == nil {
			t.Errorf("%v % X decoded to %v", r.PID, r.Data, v)
		}
	}
}

func TestParseReadings(t *testing.T) {
	// Response to "01 0C 0D" after the 0x41.
	rs, err := parseReadings(0x7E8, []byte{0x0C, 0x1A, 0xF8, 0x0D, 0x32}, false)
	if err != nil || len(rs) != 2 || rs[0].PID != EngineRPM || rs[1].PID != VehicleSpeed ||
		!slices.Equal(rs[0].Data, []byte{0x1A, 0xF8}) || !slices.Equal(rs[1].Data, []byte{0x32}) {
		t.Fatalf("multi-PID: %v, %v", rs, err)
	}

	// A single-PID answer keeps bytes beyond the usual size (here bank 3).
	rs, err = parseReadings(0x7E8, []byte{0x06, 0x80, 0x84}, true)
	if err != nil || len(rs) != 1 || len(rs[0].Data) != 2 {
		t.Fatalf("single PID: %v, %v", rs, err)
	}

	if _, err := parseReadings(0x7E8, []byte{0x0C, 0x1A}, false); err == nil {
		t.Error("truncated response parsed without error")
	}
}

func TestSupportedFromBitmap(t *testing.T) {
	got := supportedFromBitmap(0x00, []byte{0xBE, 0x1F, 0xA8, 0x13})
	want := []PID{0x01, 0x03, 0x04, 0x05, 0x06, 0x07, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x13, 0x15, 0x1C, 0x1F, 0x20}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPIDNames(t *testing.T) {
	tests := map[PID]string{
		EngineRPM:           "Engine speed",
		0x20:                "PIDs supported [21-40]",
		O2Sensor1 + 2:       "Oxygen sensor 3 (voltage, short term fuel trim)",
		PID(0xFE):           "PID FE",
		MonitorStatus:       "Monitor status since DTCs cleared",
		FuelTankLevel:       "Fuel tank level input",
		CoolantTemp:         "Engine coolant temperature",
		PIDsSupported01To20: "PIDs supported [01-20]",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("PID %02X = %q, want %q", byte(p), got, want)
		}
	}
}
