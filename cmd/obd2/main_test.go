package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/ecusim"
)

// client returns a client for a simulated engine ECU idling on a virtual bus.
func client(t *testing.T) *obd2.Client {
	t.Helper()
	bus := can.NewVirtualBus()
	ctx, cancel := context.WithCancel(context.Background())
	ecuPort, tester := bus.Connect(), bus.Connect()
	t.Cleanup(func() { _ = tester.Close(); _ = ecuPort.Close(); cancel() })
	engine := &ecusim.ECU{
		Address: 0x7E0,
		PIDs: map[obd2.PID][]byte{
			obd2.MonitorStatus: {0x82, 0x07, 0x65, 0x00}, // check engine light on, 2 codes
			obd2.EngineRPM:     {0x0B, 0x64},
			obd2.VehicleSpeed:  {0x00},
		},
		Stored: []obd2.DTC{0x0300, 0x0133},
		VIN:    "1M8GDM9AXKP042788",
	}
	go func() { _ = engine.Serve(ctx, ecuPort) }()
	return obd2.NewClient(obd2.NewCANTransport(tester, obd2.CANOptions{Timeout: 20 * time.Millisecond}))
}

func runCmd(t *testing.T, asJSON bool, cmd string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	opt := options{timeout: 5 * time.Second, json: asJSON}
	if err := run(context.Background(), client(t), &buf, opt, cmd, args); err != nil {
		t.Fatalf("%s %v: %v", cmd, args, err)
	}
	return buf.String()
}

func decode(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("not JSON: %q: %v", s, err)
	}
}

func TestReadJSON(t *testing.T) {
	var got struct {
		Time     string
		Readings []struct {
			ECU, PID, Name, Unit, Raw string
			Value                     *float64
		}
	}
	decode(t, runCmd(t, true, "read", "0C", "0D"), &got)
	if _, err := time.Parse(time.RFC3339Nano, got.Time); err != nil {
		t.Errorf("time %q: %v", got.Time, err)
	}
	if len(got.Readings) != 2 {
		t.Fatalf("readings = %+v", got.Readings)
	}
	rpm := got.Readings[0]
	if rpm.ECU != "7E8" || rpm.PID != "0C" || rpm.Name != "Engine speed" || rpm.Unit != "rpm" ||
		rpm.Raw != "0B64" || rpm.Value == nil || *rpm.Value != 729 {
		t.Errorf("engine speed = %+v", rpm)
	}
	if s := got.Readings[1]; s.PID != "0D" || s.Value == nil || *s.Value != 0 || s.Unit != "km/h" {
		t.Errorf("vehicle speed = %+v", s)
	}
}

func TestReadText(t *testing.T) {
	out := runCmd(t, false, "read", "0C")
	if want := "7E8      Engine speed: 729 rpm\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestInfoJSON(t *testing.T) {
	var got struct {
		Protocol string
		PIDs     []struct{ PID, Name string }
	}
	decode(t, runCmd(t, true, "info"), &got)
	if got.Protocol != "ISO 15765-4 CAN (11-bit)" {
		t.Errorf("protocol = %q", got.Protocol)
	}
	if !slices.ContainsFunc(got.PIDs, func(p struct{ PID, Name string }) bool {
		return p.PID == "0C" && p.Name == "Engine speed"
	}) {
		t.Errorf("pids = %+v", got.PIDs)
	}
}

func TestDTCJSON(t *testing.T) {
	var got struct {
		MIL       bool
		Confirmed int
		Stored    []string
		Pending   []string
		Permanent []string
	}
	decode(t, runCmd(t, true, "dtc"), &got)
	if !got.MIL || got.Confirmed != 2 || !slices.Equal(got.Stored, []string{"P0133", "P0300"}) ||
		got.Pending == nil || len(got.Pending) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestVINAndRawJSON(t *testing.T) {
	var vin struct{ VIN string }
	decode(t, runCmd(t, true, "vin"), &vin)
	if vin.VIN != "1M8GDM9AXKP042788" {
		t.Errorf("vin = %q", vin.VIN)
	}
	var raw struct{ Responses []struct{ ECU, Data string } }
	decode(t, runCmd(t, true, "raw", "0902"), &raw)
	if len(raw.Responses) != 1 || raw.Responses[0].ECU != "7E8" || !strings.HasPrefix(raw.Responses[0].Data, "490201") {
		t.Errorf("raw = %+v", raw)
	}
}

func TestClearNeedsConfirmation(t *testing.T) {
	err := run(context.Background(), client(t), &bytes.Buffer{}, options{timeout: time.Second}, "clear", nil)
	if err == nil || !strings.Contains(err.Error(), "-yes") {
		t.Errorf("err = %v", err)
	}
}
