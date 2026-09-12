package obd2_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/RyoheiHashimoto/obd2"
)

// pidServer answers service 01 requests from a table, as an ECU behind an
// adapter would. With firstOnly it answers only the first PID of a
// multi-PID request, as many ELM327 clones do.
type pidServer struct {
	data      map[obd2.PID][]byte
	firstOnly bool
	sent      []string
}

func (s *pidServer) RoundTrip(ctx context.Context, req []byte) ([]obd2.Response, error) {
	s.sent = append(s.sent, fmt.Sprintf("%X", req))
	if len(req) < 2 || req[0] != 0x01 {
		return nil, obd2.ErrNoResponse
	}
	pids := req[1:]
	if s.firstOnly {
		pids = pids[:1]
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

// singlePID is a pidServer whose transport cannot combine PIDs, like the
// elm327 adapter.
type singlePID struct{ *pidServer }

func (singlePID) CombinesPIDs() bool { return false }

func idleDemio() map[obd2.PID][]byte {
	return map[obd2.PID][]byte{
		obd2.EngineRPM:    {0x0B, 0x64},
		obd2.VehicleSpeed: {0x00},
		obd2.CoolantTemp:  {0x7D},
	}
}

// queryAfterLearning makes a first query, which teaches the client the
// protocol, then records what the second query sends.
func queryAfterLearning(t *testing.T, c *obd2.Client, s *pidServer, pids ...obd2.PID) (obd2.Readings, []string) {
	t.Helper()
	if _, err := c.Query(ctx(t), obd2.EngineRPM); err != nil {
		t.Fatal(err)
	}
	s.sent = nil
	rs, err := c.Query(ctx(t), pids...)
	if err != nil {
		t.Fatal(err)
	}
	return rs, s.sent
}

func TestQueryCombinesPIDsOnCAN(t *testing.T) {
	s := &pidServer{data: idleDemio()}
	rs, sent := queryAfterLearning(t, obd2.NewClient(s), s, obd2.EngineRPM, obd2.VehicleSpeed, obd2.CoolantTemp)
	if len(rs) != 3 || !slices.Equal(sent, []string{"010C0D05"}) {
		t.Errorf("got %d readings, sent %q; want 3 in one request", len(rs), sent)
	}
}

// The ELM327 v1.5 clone once used by pi-obd-meter answered only one PID of
// a multi-PID request (RyoheiHashimoto/pi-obd-meter#54). A transport that
// says it cannot combine PIDs gets them one at a time, and nothing is lost.
func TestQueryAsksOneAtATimeWhenTransportCannotCombine(t *testing.T) {
	s := &pidServer{data: idleDemio(), firstOnly: true}
	rs, sent := queryAfterLearning(t, obd2.NewClient(singlePID{s}), s, obd2.EngineRPM, obd2.VehicleSpeed, obd2.CoolantTemp)
	if len(rs) != 3 || !slices.Equal(sent, []string{"010C", "010D", "0105"}) {
		t.Errorf("got %d readings, sent %q; want 3 readings, one PID per request", len(rs), sent)
	}
}
