package obd2

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// maxPIDsPerRequest is the most PIDs one service 01 request may carry. Only
// CAN allows more than one (SAE J1979).
const maxPIDsPerRequest = 6

// Client speaks the standard OBD-II diagnostic services (SAE J1979) through a
// Transport. It is safe for concurrent use; requests are sent one at a time.
type Client struct {
	t Transport

	mu    sync.Mutex
	proto Protocol   // learned from the first response
	batch batchState // whether the transport handles multi-PID requests
}

// NewClient returns a Client that sends its requests through t.
func NewClient(t Transport) *Client {
	return &Client{t: t}
}

// Protocol returns the protocol the vehicle answered on, or ProtocolUnknown
// before the first answer.
func (c *Client) Protocol() Protocol {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proto
}

// Request sends a raw request, a service ID followed by its parameters, and
// returns every ECU's response ordered by ECU. Negative responses are
// included; see Response.Err. Responses that cannot answer req are dropped:
// ECUs send them when another tester shares the bus.
func (c *Client) Request(ctx context.Context, req []byte) ([]Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs, err := c.t.RoundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	rs = slices.DeleteFunc(rs, func(r Response) bool { return !answers(req, r.Data) })
	if len(rs) == 0 {
		return nil, ErrNoResponse
	}
	slices.SortStableFunc(rs, func(a, b Response) int { return int(a.ECU) - int(b.ECU) })
	if rs[0].Protocol != ProtocolUnknown {
		c.proto = rs[0].Protocol
	}
	return rs, nil
}

// positive keeps the responses that answer service sid. If there are none
// but an ECU refused the request, it returns that refusal.
func positive(rs []Response, sid byte) ([]Response, error) {
	var out []Response
	var refusal error
	for _, r := range rs {
		if len(r.Data) > 0 && r.Data[0] == sid+0x40 {
			out = append(out, r)
		} else if err := r.Err(); err != nil && refusal == nil {
			refusal = err
		}
	}
	if len(out) == 0 {
		if refusal != nil {
			return nil, refusal
		}
		return nil, ErrNoResponse
	}
	return out, nil
}

// Query reads the current value of the given service 01 PIDs.
//
// The result holds one reading per PID and ECU that answered, ordered as the
// PIDs were given and then by ECU. PIDs that no ECU supports are left out.
// Query fails with ErrNoResponse only if no ECU answered at all.
//
// On CAN, up to six PIDs whose sizes this package knows are combined into
// one request. The other protocols allow one PID per request. Many ELM327
// clones answer only the first PID of a combined request; the client
// notices this on its first combined request and asks for one PID at a
// time from then on.
func (c *Client) Query(ctx context.Context, pids ...PID) (Readings, error) {
	var out Readings
	answered := false
	for rest := pids; len(rest) > 0; {
		n := c.batchSize(rest)
		batch := rest[:n]
		rest = rest[n:]
		rs, err := c.service01(ctx, batch)
		if len(batch) > 1 && c.batching() == batchUntested {
			rs, err = c.checkBatching(ctx, batch, rs, err)
		}
		if errors.Is(err, ErrNoResponse) {
			continue
		}
		if err != nil {
			return out, err
		}
		answered = true
		out = append(out, rs...)
	}
	if !answered && len(pids) > 0 {
		return nil, ErrNoResponse
	}
	slices.SortStableFunc(out, func(a, b Reading) int {
		if d := slices.Index(pids, a.PID) - slices.Index(pids, b.PID); d != 0 {
			return d
		}
		return int(a.ECU) - int(b.ECU)
	})
	return out, nil
}

// batchSize returns how many of the leading PIDs to put in one request. The
// first request goes out alone, since the protocol is not yet known.
func (c *Client) batchSize(pids []PID) int {
	if !c.Protocol().IsCAN() || c.batching() == batchBroken {
		return 1
	}
	n := 0
	for n < len(pids) && n < maxPIDsPerRequest {
		if _, ok := pids[n].Info(); !ok {
			break
		}
		n++
	}
	return max(n, 1)
}

func (c *Client) service01(ctx context.Context, pids []PID) (Readings, error) {
	req := []byte{0x01}
	for _, p := range pids {
		req = append(req, byte(p))
	}
	rs, err := c.Request(ctx, req)
	if err != nil {
		return nil, err
	}
	pos, err := positive(rs, 0x01)
	if err != nil {
		return nil, err
	}
	// With another tester on the bus, an ECU may answer the same PID twice
	// within one request, once for each tester. Keep the first answer, and
	// do not let a malformed answer spoil the others.
	type key struct {
		ecu uint32
		pid PID
	}
	seen := map[key]bool{}
	var out Readings
	var parseErr error
	for _, r := range pos {
		readings, err := parseReadings(r.ECU, r.Data[1:], len(pids) == 1)
		if err != nil {
			parseErr = cmp.Or(parseErr, err)
			continue
		}
		for _, rd := range readings {
			k := key{rd.ECU, rd.PID}
			if slices.Contains(pids, rd.PID) && !seen[k] {
				seen[k] = true
				out = append(out, rd)
			}
		}
	}
	if len(out) == 0 {
		return nil, cmp.Or(parseErr, ErrNoResponse)
	}
	return out, nil
}

// SupportedPIDs asks the ECUs which service 01 PIDs they support and returns
// the union in ascending order.
func (c *Client) SupportedPIDs(ctx context.Context) ([]PID, error) {
	var out []PID
	for base := PIDsSupported01To20; ; base += 0x20 {
		rs, err := c.service01(ctx, []PID{base})
		if err != nil {
			if base == PIDsSupported01To20 {
				return nil, err
			}
			break
		}
		more := false
		for _, r := range rs {
			for _, p := range supportedFromBitmap(base, r.Data) {
				if !slices.Contains(out, p) {
					out = append(out, p)
				}
			}
			more = more || slices.Contains(supportedFromBitmap(base, r.Data), base+0x20)
		}
		if !more || base == 0xE0 {
			break
		}
	}
	slices.Sort(out)
	return out, nil
}

// MILStatus reports whether any ECU has the malfunction indicator lamp (the
// check engine light) on, and the number of confirmed DTCs the ECUs report
// in total. It reads PID 01.
func (c *Client) MILStatus(ctx context.Context) (on bool, dtcCount int, err error) {
	rs, err := c.service01(ctx, []PID{MonitorStatus})
	if err != nil {
		return false, 0, err
	}
	for _, r := range rs {
		if len(r.Data) > 0 {
			on = on || r.Data[0]&0x80 != 0
			dtcCount += int(r.Data[0] & 0x7F)
		}
	}
	return on, dtcCount, nil
}

// StoredDTCs reads the confirmed trouble codes (service 03).
func (c *Client) StoredDTCs(ctx context.Context) ([]DTC, error) { return c.dtcs(ctx, 0x03) }

// PendingDTCs reads the codes detected during the current or last drive
// cycle but not yet confirmed (service 07).
func (c *Client) PendingDTCs(ctx context.Context) ([]DTC, error) { return c.dtcs(ctx, 0x07) }

// PermanentDTCs reads the codes that clearing cannot erase; the ECU removes
// them itself once the fault is gone (service 0A).
func (c *Client) PermanentDTCs(ctx context.Context) ([]DTC, error) { return c.dtcs(ctx, 0x0A) }

// dtcs returns the codes all ECUs report for sid, sorted and without
// duplicates. Use Request and ParseDTCs to tell the ECUs apart.
func (c *Client) dtcs(ctx context.Context, sid byte) ([]DTC, error) {
	rs, err := c.Request(ctx, []byte{sid})
	if err != nil {
		return nil, err
	}
	pos, err := positive(rs, sid)
	if err != nil {
		return nil, err
	}
	out := []DTC{}
	for _, r := range pos {
		codes, err := ParseDTCs(r)
		if err != nil {
			return nil, err
		}
		for _, d := range codes {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// ClearDTCs erases the trouble codes and freeze frames in every ECU and
// turns the MIL off (service 04). It also resets the readiness monitors, so
// the vehicle may fail an emissions inspection until it has been driven
// long enough for them to complete again.
func (c *Client) ClearDTCs(ctx context.Context) error {
	rs, err := c.Request(ctx, []byte{0x04})
	if err != nil {
		return err
	}
	_, err = positive(rs, 0x04)
	return err
}

// VIN reads the vehicle identification number (service 09, PID 02) from the
// first ECU that answers. Vehicles built for the Japanese market may return
// their shorter chassis number (model code and serial) instead.
func (c *Client) VIN(ctx context.Context) (string, error) {
	rs, err := c.Request(ctx, []byte{0x09, 0x02})
	if err != nil {
		return "", err
	}
	pos, err := positive(rs, 0x09)
	if err != nil {
		return "", err
	}
	ecu := pos[0].ECU
	var raw []byte
	if pos[0].Protocol.IsCAN() || pos[0].Protocol == ProtocolUnknown {
		// 49 02, the number of data items (01), then the 17 characters.
		raw = pos[0].Data[2:]
	} else {
		// Five messages: 49 02, a sequence number, then 4 bytes each.
		var msgs []Response
		for _, r := range pos {
			if r.ECU == ecu && len(r.Data) > 3 {
				msgs = append(msgs, r)
			}
		}
		slices.SortFunc(msgs, func(a, b Response) int { return int(a.Data[2]) - int(b.Data[2]) })
		for _, m := range msgs {
			raw = append(raw, m.Data[3:]...)
		}
	}
	// Keep the printable characters: this drops the item count and the
	// 00 filler that J1979 puts before short VINs.
	vin := bytes.Map(func(r rune) rune {
		if r > ' ' && r < 0x7F {
			return r
		}
		return -1
	}, raw)
	if len(vin) == 0 {
		return "", fmt.Errorf("obd2: ECU %X sent an empty VIN", ecu)
	}
	return string(vin), nil
}

// ReadDataByIdentifier reads a data identifier with service 22, which
// manufacturers use for data beyond the standard PIDs. It returns the data
// that follows the identifier in the first positive response.
//
// Some ECUs ignore service 22 when it is broadcast; address the ECU directly
// (see CANOptions.ECU) if no answer comes.
func (c *Client) ReadDataByIdentifier(ctx context.Context, did uint16) ([]byte, error) {
	rs, err := c.Request(ctx, []byte{0x22, byte(did >> 8), byte(did)})
	if err != nil {
		return nil, err
	}
	pos, err := positive(rs, 0x22)
	if err != nil {
		return nil, err
	}
	for _, r := range pos {
		if len(r.Data) >= 3 && r.Data[1] == byte(did>>8) && r.Data[2] == byte(did) {
			return r.Data[3:], nil
		}
	}
	return nil, fmt.Errorf("obd2: no response carried DID %04X", did)
}

// batchState records whether the transport handles multi-PID requests.
type batchState int

const (
	batchUntested batchState = iota
	batchWorks
	batchBroken
)

func (c *Client) batching() batchState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.batch
}

// checkBatching runs after the first multi-PID request. Many ELM327 clones
// answer only the first PID of such a request, or refuse it. The missing
// PIDs are asked for one at a time; if one answers alone, or if the request
// was refused only when combined, the client stops combining PIDs.
func (c *Client) checkBatching(ctx context.Context, batch []PID, rs Readings, err error) (Readings, error) {
	var missing []PID
	for _, p := range batch {
		if !slices.ContainsFunc(rs, func(r Reading) bool { return r.PID == p }) {
			missing = append(missing, p)
		}
	}
	broken := err != nil && !errors.Is(err, ErrNoResponse)
	for _, p := range missing {
		single, serr := c.service01(ctx, []PID{p})
		switch {
		case serr == nil:
			broken = true
			rs = append(rs, single...)
		case !errors.Is(serr, ErrNoResponse):
			return rs, serr
		}
	}
	c.mu.Lock()
	c.batch = batchWorks
	if broken {
		c.batch = batchBroken
	}
	c.mu.Unlock()
	if len(rs) == 0 {
		return nil, ErrNoResponse
	}
	return rs, nil
}
