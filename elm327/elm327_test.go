package elm327_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/elm327"
)

// fakeELM answers commands from a script, the way an adapter with headers
// on and spaces off would. The answers below follow the examples in the
// ELM327 data sheet (ELM327DSJ), with the spaces removed.
type fakeELM struct {
	script map[string]string
	late   map[string]time.Duration // answers sent after a delay

	mu      sync.Mutex
	pending []byte
	cmds    []string
	r       *io.PipeReader
	w       *io.PipeWriter
}

func newFake(script map[string]string) *fakeELM {
	r, w := io.Pipe()
	base := map[string]string{
		"ATZ":  "\r\rELM327 v1.5",
		"ATE0": "ATE0\rOK", // echo is still on for this one
		"ATL0": "OK",
		"ATS0": "OK",
		"ATH1": "OK",
	}
	for k, v := range script {
		base[k] = v
	}
	return &fakeELM{script: base, late: map[string]time.Duration{}, r: r, w: w}
}

func (f *fakeELM) Read(p []byte) (int, error) { return f.r.Read(p) }

func (f *fakeELM) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.pending = append(f.pending, p...)
	var cmds []string
	for {
		i := bytes.IndexByte(f.pending, '\r')
		if i < 0 {
			break
		}
		cmds = append(cmds, string(f.pending[:i]))
		f.pending = f.pending[i+1:]
	}
	f.cmds = append(f.cmds, cmds...)
	f.mu.Unlock()

	for _, cmd := range cmds {
		ans, ok := f.script[cmd]
		if !ok {
			ans = "?"
		}
		out := []byte(ans + "\r\r>")
		if d, ok := f.late[cmd]; ok {
			go func() {
				time.Sleep(d)
				_, _ = f.w.Write(out)
			}()
			continue
		}
		_, _ = f.w.Write(out)
	}
	return len(p), nil
}

func (f *fakeELM) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

func open(t *testing.T, f *fakeELM, opt elm327.Options) (*elm327.Adapter, *obd2.Client) {
	t.Helper()
	a, err := elm327.Open(testCtx(t), f, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.w.Close() })
	return a, obd2.NewClient(a)
}

func testCtx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

var canScript = map[string]string{
	"ATTPA6": "OK",
	"ATDPN":  "A6",
	// First request: the adapter searches, then answers.
	"0100": "SEARCHING...\r7E8064100BE3FB81300",
	"0120": "NO DATA",
	"0101": "7E80641018207650000",
	"010C": "7E804410C1AF8000000",
	// Two PIDs in one request, as CAN allows.
	"010C0D": "7E806410C1AF80D3200",
	"03":     "7E80643020133030000",
	// The VIN of the data sheet: a first frame and two consecutive frames.
	"0902": "7E81014490201314434\r7E82147503030523535\r7E82242313233343536",
	// The data sheet's example of two ECUs whose frames interleave.
	"0904": "7E81013490401353630\r7E82132383934394143\r7E91013490401353630\r" +
		"7E82200000000000031\r7E92132383935344143\r7E92200000000000000",
	"0105": "CAN ERROR",
	"0146": "NO DATA",
}

func TestCAN(t *testing.T) {
	f := newFake(canScript)
	a, c := open(t, f, elm327.Options{})
	ctx := testCtx(t)
	if a.Version() != "ELM327 v1.5" {
		t.Errorf("version = %q", a.Version())
	}

	pids, err := c.SupportedPIDs(ctx)
	if err != nil || len(pids) == 0 || pids[0] != 0x01 {
		t.Fatalf("SupportedPIDs = %v, %v", pids, err)
	}
	if c.Protocol() != obd2.ProtocolCAN11 {
		t.Errorf("protocol = %v", c.Protocol())
	}

	rs, err := c.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rs.Float(obd2.EngineRPM); v != 1726 {
		t.Errorf("rpm = %v", v)
	}
	if v, _ := rs.Float(obd2.VehicleSpeed); v != 50 {
		t.Errorf("speed = %v", v)
	}
	if !slices.Contains(f.sent(), "010C0D") {
		t.Errorf("PIDs were not combined on CAN; sent %q", f.sent())
	}

	on, n, err := c.MILStatus(ctx)
	if err != nil || !on || n != 2 {
		t.Errorf("MILStatus = %v, %d, %v", on, n, err)
	}
	codes, err := c.StoredDTCs(ctx)
	if err != nil || !slices.Equal(codes, []obd2.DTC{0x0133, 0x0300}) {
		t.Errorf("StoredDTCs = %v, %v", codes, err)
	}
	vin, err := c.VIN(ctx)
	if err != nil || vin != "1D4GP00R55B123456" {
		t.Errorf("VIN = %q, %v", vin, err)
	}
}

func TestCANInterleavedECUs(t *testing.T) {
	_, c := open(t, newFake(canScript), elm327.Options{})
	rs, err := c.Request(testCtx(t), []byte{0x09, 0x04})
	if err != nil {
		t.Fatal(err)
	}
	calib := []byte{0x49, 0x04, 0x01, 0x35, 0x36, 0x30, 0x32, 0x38, 0x39}
	want := []obd2.Response{
		{ECU: 0x7E8, Protocol: obd2.ProtocolCAN11, Data: append(slices.Clone(calib), 0x34, 0x39, 0x41, 0x43, 0, 0, 0, 0, 0, 0)},
		{ECU: 0x7E9, Protocol: obd2.ProtocolCAN11, Data: append(slices.Clone(calib), 0x35, 0x34, 0x41, 0x43, 0, 0, 0, 0, 0, 0)},
	}
	if len(rs) != 2 {
		t.Fatalf("got %d responses: %v", len(rs), rs)
	}
	for i := range want {
		if rs[i].ECU != want[i].ECU || rs[i].Protocol != want[i].Protocol || !bytes.Equal(rs[i].Data, want[i].Data) {
			t.Errorf("response %d = %X % X, want %X % X", i, rs[i].ECU, rs[i].Data, want[i].ECU, want[i].Data)
		}
	}
}

func TestAdapterMessages(t *testing.T) {
	_, c := open(t, newFake(canScript), elm327.Options{})
	ctx := testCtx(t)
	if _, err := c.Query(ctx, obd2.CoolantTemp); !errors.Is(err, elm327.ErrAdapter) || !strings.Contains(err.Error(), "CAN ERROR") {
		t.Errorf("CAN ERROR: err = %v", err)
	}
	if _, err := c.Query(ctx, obd2.AmbientAirTemp); !errors.Is(err, obd2.ErrNoResponse) {
		t.Errorf("NO DATA: err = %v", err)
	}
}

func TestAbandonedCommandIsResynchronized(t *testing.T) {
	f := newFake(canScript)
	f.late["0105"] = 150 * time.Millisecond
	f.script["0105"] = "7E803410546000000" // arrives after the caller gave up
	_, c := open(t, f, elm327.Options{})

	short, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Query(short, obd2.CoolantTemp); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	// The next command must get its own answer, not the late one.
	rs, err := c.Query(testCtx(t), obd2.EngineRPM)
	if err != nil || len(rs) != 1 || rs[0].PID != obd2.EngineRPM {
		t.Errorf("after resync: %v, %v", rs, err)
	}
	if slices.Contains(f.sent(), "") {
		t.Error("resync sent an empty line, which repeats the last command")
	}
}

func TestISO9141(t *testing.T) {
	f := newFake(map[string]string{
		"ATTP3": "OK",
		"ATDPN": "3",
		// The data sheet's example of two ECUs (10 and 18) answering 01 00.
		"0100": "486B104100BE3EB811FA\r486B18410080108000C0",
		"010C": "486B10410C1AF8AA",
		"010D": "486B10410D32AA",
		// A VIN arrives as five messages with a sequence number each.
		"0902": "486B1049020100000031AA\r486B1049020244344750AA\r486B1049020330305235AA\r" +
			"486B1049020435423132AA\r486B1049020533343536AA",
		// No count byte; unused slots are 0000.
		"03": "486B1043013300000000AA",
	})
	_, c := open(t, f, elm327.Options{Protocol: elm327.ProtocolISO9141})
	ctx := testCtx(t)

	rs, err := c.Request(ctx, []byte{0x01, 0x00})
	if err != nil || len(rs) != 2 || rs[0].ECU != 0x10 || rs[1].ECU != 0x18 ||
		!bytes.Equal(rs[0].Data, []byte{0x41, 0x00, 0xBE, 0x3E, 0xB8, 0x11}) {
		t.Fatalf("Request = %v, %v", rs, err)
	}
	if c.Protocol() != obd2.ProtocolISO9141 {
		t.Errorf("protocol = %v", c.Protocol())
	}

	readings, err := c.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed)
	if err != nil || len(readings) != 2 {
		t.Fatalf("Query = %v, %v", readings, err)
	}
	if slices.Contains(f.sent(), "010C0D") {
		t.Error("PIDs were combined on a protocol that allows one per request")
	}

	vin, err := c.VIN(ctx)
	if err != nil || vin != "1D4GP00R55B123456" {
		t.Errorf("VIN = %q, %v", vin, err)
	}
	codes, err := c.StoredDTCs(ctx)
	if err != nil || !slices.Equal(codes, []obd2.DTC{0x0133}) {
		t.Errorf("StoredDTCs = %v, %v", codes, err)
	}
}

func TestOpenDoesNotSaveTheProtocol(t *testing.T) {
	f := newFake(canScript)
	open(t, f, elm327.Options{})
	for _, c := range f.sent() {
		if strings.HasPrefix(c, "ATSP") {
			t.Errorf("sent %q, which saves the protocol in the adapter", c)
		}
	}
	if !slices.Contains(f.sent(), "ATTPA6") {
		t.Errorf("did not start the search with CAN 11-bit; sent %q", f.sent())
	}
}

func TestOpenFallsBackToPlainSearch(t *testing.T) {
	// An adapter that does not know TP A answers "?".
	f := newFake(map[string]string{"ATSP0": "OK"})
	open(t, f, elm327.Options{})
	if sent := f.sent(); !slices.Contains(sent, "ATTPA6") || !slices.Contains(sent, "ATSP0") {
		t.Errorf("sent %q", sent)
	}
}
