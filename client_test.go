package obd2_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/ecusim"
)

// vehicle connects the ECUs to a virtual bus and returns a client for it.
func vehicle(t *testing.T, opt obd2.CANOptions, ecus ...*ecusim.ECU) *obd2.Client {
	t.Helper()
	bus := can.NewVirtualBus()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, e := range ecus {
		port := bus.Connect()
		t.Cleanup(func() { _ = port.Close() })
		go func() { _ = e.Serve(ctx, port) }()
	}
	tester := bus.Connect()
	t.Cleanup(func() { _ = tester.Close() })
	return obd2.NewClient(obd2.NewCANTransport(tester, opt))
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func engine() *ecusim.ECU {
	return &ecusim.ECU{
		Address: 0x7E0,
		PIDs: map[obd2.PID][]byte{
			obd2.MonitorStatus:  {0x82, 0x07, 0x65, 0x00}, // MIL on, 2 codes
			obd2.CoolantTemp:    {0x7B},
			obd2.EngineRPM:      {0x1A, 0xF8},
			obd2.VehicleSpeed:   {0x32},
			obd2.FuelTankLevel:  {0x80},
			obd2.AmbientAirTemp: {0x3C},
		},
		Stored:  []obd2.DTC{0x0300, 0x0133},
		Pending: []obd2.DTC{0x0171},
		VIN:     "1M8GDM9AXKP042788",
		DIDs:    map[uint16][]byte{0x17B3: {0x5A}},
	}
}

func transmission() *ecusim.ECU {
	return &ecusim.ECU{
		Address: 0x7E1,
		PIDs: map[obd2.PID][]byte{
			obd2.MonitorStatus: {0x01, 0x00, 0x00, 0x00},
			obd2.VehicleSpeed:  {0x31},
		},
		Stored: []obd2.DTC{0x0700},
	}
}

func TestQueryAcrossECUs(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{}, engine(), transmission())
	rs, err := c.Query(ctx(t), obd2.EngineRPM, obd2.VehicleSpeed, obd2.CoolantTemp)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs {
		got = append(got, r.String())
	}
	want := []string{
		"Engine speed: 1726 rpm",
		"Vehicle speed: 50 km/h", // engine ECU (7E8) first
		"Vehicle speed: 49 km/h", // then the transmission (7E9)
		"Engine coolant temperature: 83 °C",
	}
	if !slices.Equal(got, want) {
		t.Errorf("readings:\n got %q\nwant %q", got, want)
	}
	if v, err := rs.Float(obd2.VehicleSpeed); err != nil || v != 50 {
		t.Errorf("Float(VehicleSpeed) = %v, %v", v, err)
	}
	if p := c.Protocol(); p != obd2.ProtocolCAN11 {
		t.Errorf("protocol = %v", p)
	}
}

func TestQueryUnsupportedPID(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{Timeout: 20 * time.Millisecond}, engine())
	rs, err := c.Query(ctx(t), obd2.EngineRPM, obd2.EngineOilTemp)
	if err != nil || len(rs) != 1 || rs[0].PID != obd2.EngineRPM {
		t.Errorf("Query = %v, %v", rs, err)
	}
	if _, err := c.Query(ctx(t), obd2.EngineOilTemp); !errors.Is(err, obd2.ErrNoResponse) {
		t.Errorf("unsupported PID alone: err = %v", err)
	}
}

func TestSupportedPIDs(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{}, engine(), transmission())
	got, err := c.SupportedPIDs(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []obd2.PID{0x01, 0x05, 0x0C, 0x0D, 0x20, 0x2F, 0x40, 0x46}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestDTCs(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{}, engine(), transmission())
	ctx := ctx(t)

	on, n, err := c.MILStatus(ctx)
	if err != nil || !on || n != 3 {
		t.Errorf("MILStatus = %v, %d, %v", on, n, err)
	}
	stored, err := c.StoredDTCs(ctx)
	if want := []obd2.DTC{0x0133, 0x0300, 0x0700}; err != nil || !slices.Equal(stored, want) {
		t.Errorf("StoredDTCs = %v, %v; want %v", stored, err, want)
	}
	pending, err := c.PendingDTCs(ctx)
	if err != nil || !slices.Equal(pending, []obd2.DTC{0x0171}) {
		t.Errorf("PendingDTCs = %v, %v", pending, err)
	}
	if err := c.ClearDTCs(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = c.StoredDTCs(ctx)
	if err != nil || len(stored) != 0 {
		t.Errorf("after ClearDTCs: %v, %v", stored, err)
	}
}

func TestVIN(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{}, engine(), transmission())
	vin, err := c.VIN(ctx(t))
	if err != nil || vin != "1M8GDM9AXKP042788" {
		t.Errorf("VIN = %q, %v", vin, err)
	}
}

func TestAddressing29BitDetected(t *testing.T) {
	e := engine()
	e.Address, e.Extended = 0x10, true
	c := vehicle(t, obd2.CANOptions{Timeout: 30 * time.Millisecond}, e)
	ctx := ctx(t)
	rs, err := c.Query(ctx, obd2.EngineRPM)
	if err != nil || len(rs) != 1 || rs[0].ECU != 0x18DAF110 {
		t.Fatalf("Query = %v, %v", rs, err)
	}
	if p := c.Protocol(); p != obd2.ProtocolCAN29 {
		t.Errorf("protocol = %v", p)
	}
	if vin, err := c.VIN(ctx); err != nil || vin != e.VIN {
		t.Errorf("VIN over 29-bit = %q, %v", vin, err)
	}
}

func TestDirectedRequestReturnsEarly(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{ECU: 0x7E0, Timeout: time.Second}, engine(), transmission())
	start := time.Now()
	rs, err := c.Query(ctx(t), obd2.VehicleSpeed)
	if err != nil || len(rs) != 1 || rs[0].ECU != 0x7E8 {
		t.Fatalf("Query = %v, %v", rs, err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("directed request took %v; it should not wait for the timeout", el)
	}
}

func TestReadDataByIdentifier(t *testing.T) {
	e := engine()
	e.PendingReplies = 2
	c := vehicle(t, obd2.CANOptions{ECU: 0x7E0}, e)
	ctx := ctx(t)
	d, err := c.ReadDataByIdentifier(ctx, 0x17B3)
	if err != nil || !slices.Equal(d, []byte{0x5A}) {
		t.Errorf("DID 17B3 = % X, %v", d, err)
	}
	_, err = c.ReadDataByIdentifier(ctx, 0x1234)
	var nrc *obd2.NegativeResponseError
	if !errors.As(err, &nrc) || nrc.Code != 0x31 || nrc.Service != 0x22 {
		t.Errorf("unknown DID: err = %v", err)
	}
}

func TestNoVehicle(t *testing.T) {
	c := vehicle(t, obd2.CANOptions{Timeout: 20 * time.Millisecond})
	if _, err := c.Query(ctx(t), obd2.EngineRPM); !errors.Is(err, obd2.ErrNoResponse) {
		t.Errorf("err = %v, want ErrNoResponse", err)
	}
}
