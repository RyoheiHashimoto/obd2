package obd2_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/ecusim"
)

// scripted is a Transport that answers each request, keyed by its hex
// form, with fixed responses.
type scripted map[string][]obd2.Response

func (s scripted) RoundTrip(ctx context.Context, req []byte) ([]obd2.Response, error) {
	if rs, ok := s[fmt.Sprintf("%X", req)]; ok {
		return rs, nil
	}
	return nil, obd2.ErrNoResponse
}

func from7E8(data ...byte) obd2.Response {
	return obd2.Response{ECU: 0x7E8, Protocol: obd2.ProtocolCAN11, Data: data}
}

// In the first test on a vehicle, the dashboard sharing the bus polled MAP
// and MAF itself, and its answers landed in the client's listening window:
// each of those PIDs came back twice from the same ECU.
func TestAnswersToAnotherTesterAreDropped(t *testing.T) {
	c := obd2.NewClient(scripted{
		"010B": {from7E8(0x41, 0x10, 0x00, 0xFE), from7E8(0x41, 0x0B, 0x28), from7E8(0x62, 0x17, 0xB3, 0x5E)},
		"0110": {from7E8(0x41, 0x10, 0x00, 0xFE), from7E8(0x41, 0x10, 0x00, 0xFB)},
	})
	rs, err := c.Query(ctx(t), obd2.IntakeManifoldPressure, obd2.MAFAirFlowRate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d readings, want one per PID and ECU: %v", len(rs), rs)
	}
	if v, _ := rs.Float(obd2.IntakeManifoldPressure); v != 40 {
		t.Errorf("MAP = %v", v)
	}
	if v, _ := rs.Float(obd2.MAFAirFlowRate); v != 2.54 {
		t.Errorf("MAF = %v, want the first answer", v)
	}
}

// A directed request returns on the first answer, so an answer meant for
// another tester must not end it.
func TestDirectedRequestSkipsOtherTestersAnswers(t *testing.T) {
	bus := can.NewVirtualBus()
	bg, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	engine := &ecusim.ECU{Address: 0x7E0, DIDs: map[uint16][]byte{0x17B3: {0x5E}}, Delay: 30 * time.Millisecond}
	ep := bus.Connect()
	go func() { _ = engine.Serve(bg, ep) }()

	// The other tester's answer, for a different DID, arrives first.
	other := bus.Connect()
	go func() {
		for {
			f, err := other.Receive(bg)
			if err != nil {
				return
			}
			if f.ID == 0x7E0 && f.Data[1] == 0x22 {
				_ = other.Send(bg, can.Frame{ID: 0x7E8, Len: 8, Data: [8]byte{0x04, 0x62, 0x11, 0x01, 0x00}})
			}
		}
	}()
	tester := bus.Connect()
	t.Cleanup(func() { _ = tester.Close(); _ = other.Close(); _ = ep.Close() })

	c := obd2.NewClient(obd2.NewCANTransport(tester, obd2.CANOptions{ECU: 0x7E0}))
	d, err := c.ReadDataByIdentifier(ctx(t), 0x17B3)
	if err != nil || !slices.Equal(d, []byte{0x5E}) {
		t.Errorf("DID 17B3 = % X, %v", d, err)
	}
}

func TestReadingStringRounds(t *testing.T) {
	for _, tt := range []struct {
		r    obd2.Reading
		want string
	}{
		{obd2.Reading{PID: obd2.EngineLoad, Data: []byte{0x48}}, "Calculated engine load: 28.24 %"},
		{obd2.Reading{PID: obd2.ControlModuleVoltage, Data: []byte{0x35, 0x62}}, "Control module voltage: 13.67 V"},
		{obd2.Reading{PID: obd2.ShortTermFuelTrimBank1, Data: []byte{0x7F}}, "Short term fuel trim, bank 1: -0.78 %"},
		{obd2.Reading{PID: obd2.EngineRPM, Data: []byte{0x0B, 0x64}}, "Engine speed: 729 rpm"},
		{obd2.Reading{PID: obd2.WarmUpsSinceCodesCleared, Data: []byte{0xFF}}, "Warm-ups since codes cleared: 255"},
	} {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}
