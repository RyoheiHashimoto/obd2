package obd2_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/RyoheiHashimoto/obd2"
)

// pidServer answers service 01 requests from a table, as an ECU behind an
// adapter would. With firstOnly it answers only the first PID of a
// multi-PID request, as many ELM327 clones do; with rejectMulti it refuses
// such requests outright.
type pidServer struct {
	data        map[obd2.PID][]byte
	firstOnly   bool
	rejectMulti bool
	sent        []string
}

var errRejected = errors.New("adapter answered ?")

func (s *pidServer) RoundTrip(ctx context.Context, req []byte) ([]obd2.Response, error) {
	s.sent = append(s.sent, fmt.Sprintf("%X", req))
	if len(req) < 2 || req[0] != 0x01 {
		return nil, obd2.ErrNoResponse
	}
	pids := req[1:]
	if len(pids) > 1 {
		if s.rejectMulti {
			return nil, errRejected
		}
		if s.firstOnly {
			pids = pids[:1]
		}
	}
	resp := []byte{0x41}
	for _, p := range pids {
		if d, ok := s.data[obd2.PID(p)]; ok {
			resp = append(append(resp, p), d...)
		}
	}
	if len(resp) == 1 {
		return nil, obd2.ErrNoResponse
	}
	return []obd2.Response{{ECU: 0x7E8, Protocol: obd2.ProtocolCAN11, Data: resp}}, nil
}

func idleDemio() map[obd2.PID][]byte {
	return map[obd2.PID][]byte{
		obd2.EngineRPM:    {0x0B, 0x64},
		obd2.VehicleSpeed: {0x00},
		obd2.CoolantTemp:  {0x7D},
	}
}

// The ELM327 v1.5 clone once used by pi-obd-meter answered only one PID of
// a multi-PID request (RyoheiHashimoto/pi-obd-meter#54).
func TestQueryFallsBackWhenMultiPIDFails(t *testing.T) {
	for _, s := range []*pidServer{
		{data: idleDemio(), firstOnly: true},
		{data: idleDemio(), rejectMulti: true},
	} {
		c := obd2.NewClient(s)
		ctx := ctx(t)
		if _, err := c.Query(ctx, obd2.EngineRPM); err != nil { // learns the protocol
			t.Fatal(err)
		}
		rs, err := c.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed, obd2.CoolantTemp)
		if err != nil || len(rs) != 3 {
			t.Fatalf("firstOnly=%v rejectMulti=%v: got %v, %v", s.firstOnly, s.rejectMulti, rs, err)
		}
		s.sent = nil
		if _, err := c.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed); err != nil {
			t.Fatal(err)
		}
		if want := []string{"010C", "010D"}; !slices.Equal(s.sent, want) {
			t.Errorf("firstOnly=%v rejectMulti=%v: sent %q, want one PID per request %q", s.firstOnly, s.rejectMulti, s.sent, want)
		}
	}
}

// An ECU that handles multi-PID requests leaves out the PIDs it lacks. The
// client checks once that they are really unsupported, then keeps
// combining PIDs.
func TestQueryKeepsCombiningWhenMultiPIDWorks(t *testing.T) {
	s := &pidServer{data: idleDemio()}
	c := obd2.NewClient(s)
	ctx := ctx(t)
	if _, err := c.Query(ctx, obd2.EngineRPM); err != nil {
		t.Fatal(err)
	}
	rs, err := c.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed, obd2.EngineOilTemp)
	if err != nil || len(rs) != 2 {
		t.Fatalf("got %v, %v", rs, err)
	}
	s.sent = nil
	if _, err := c.Query(ctx, obd2.EngineRPM, obd2.EngineOilTemp); err != nil {
		t.Fatal(err)
	}
	if want := []string{"010C5C"}; !slices.Equal(s.sent, want) {
		t.Errorf("sent %q, want %q", s.sent, want)
	}
}
