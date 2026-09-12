// Package ecusim simulates ECUs that answer OBD-II requests on a CAN bus, so
// that OBD software can be tested without a vehicle.
//
// Connect each ECU to a can.VirtualBus, or to a real bus such as a Linux
// vcan interface, and call Serve:
//
//	bus := can.NewVirtualBus()
//	engine := &ecusim.ECU{
//		Address: 0x7E0,
//		PIDs:    map[obd2.PID][]byte{obd2.EngineRPM: {0x1A, 0xF8}},
//	}
//	go engine.Serve(ctx, bus.Connect())
package ecusim

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/isotp"
)

// ECU is a simulated ECU. Set its fields before calling Serve.
type ECU struct {
	// Address is the ECU's request identifier with 11-bit identifiers
	// (0x7E0 for the engine ECU, which then answers on 0x7E8), or its
	// address byte with 29-bit identifiers (0x10 for the engine ECU).
	Address uint32
	// Extended selects 29-bit identifiers.
	Extended bool

	// PIDs holds the service 01 data for each PID: the bytes after the PID
	// in a response. The "PIDs supported" bitmaps are derived from it.
	PIDs map[obd2.PID][]byte
	// Stored, Pending and Permanent are the codes returned by services 03,
	// 07 and 0A. Service 04 clears Stored and Pending.
	Stored, Pending, Permanent []obd2.DTC
	// VIN is returned by service 09 PID 02 when it is not empty.
	VIN string
	// DIDs holds the data returned by service 22 for each identifier.
	DIDs map[uint16][]byte

	// Delay is how long the ECU waits before answering.
	Delay time.Duration
	// PendingReplies is how many "response pending" messages the ECU sends
	// before each service 22 answer.
	PendingReplies int
	// ISOTP tunes the ECU's transport layer. Extended is ignored.
	ISOTP isotp.Options

	mu sync.Mutex
}

// ids returns the identifiers the ECU receives requests on (physical and
// functional) and answers on.
func (e *ECU) ids() (physical, functional, response uint32) {
	if e.Extended {
		a := e.Address & 0xFF
		return 0x18DA00F1 | a<<8, 0x18DB33F1, 0x18DAF100 | a
	}
	return e.Address, 0x7DF, e.Address + 8
}

// Serve answers requests arriving on bus until ctx is done.
func (e *ECU) Serve(ctx context.Context, bus can.Bus) error {
	opt := e.ISOTP
	opt.Extended = e.Extended
	physical, functional, response := e.ids()
	rx := isotp.NewReceiver(opt)
	for {
		f, err := bus.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if f.Extended != e.Extended {
			continue
		}
		var req []byte
		isFunctional := false
		switch f.ID {
		case functional:
			// Functional requests are always single frames.
			p := f.Payload()
			if len(p) < 2 || p[0]>>4 != 0 || int(p[0]&0x0F) > len(p)-1 || p[0] == 0 {
				continue
			}
			req = p[1 : 1+p[0]&0x0F]
			isFunctional = true
		case physical:
			fc, msg, err := rx.Feed(f.Payload())
			if fc != nil {
				if err := bus.Send(ctx, opt.Frame(response, fc)); err != nil {
					return err
				}
			}
			if err != nil || msg == nil {
				continue
			}
			req = msg
		default:
			continue
		}

		resp := e.answer(req, isFunctional)
		if resp == nil {
			continue
		}
		if e.Delay > 0 {
			select {
			case <-time.After(e.Delay):
			case <-ctx.Done():
				return nil
			}
		}
		conn := isotp.NewConn(bus, response, physical, opt)
		if req[0] == 0x22 {
			for range e.PendingReplies {
				if err := conn.Send(ctx, []byte{0x7F, 0x22, 0x78}); err != nil {
					return err
				}
			}
		}
		if err := conn.Send(ctx, resp); err != nil && ctx.Err() != nil {
			return nil
		}
		// Other send errors mean the tester stopped listening; keep serving.
	}
}

// answer returns the response to req, or nil when the ECU stays silent.
func (e *ECU) answer(req []byte, isFunctional bool) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	sid := req[0]
	switch sid {
	case 0x01:
		resp := []byte{0x41}
		for _, p := range req[1:] {
			if d, ok := e.pidData(obd2.PID(p)); ok {
				resp = append(resp, p)
				resp = append(resp, d...)
			}
		}
		if len(resp) == 1 {
			return nil // ECUs ignore requests for PIDs they lack
		}
		return resp
	case 0x03:
		return dtcResponse(sid, e.Stored)
	case 0x07:
		return dtcResponse(sid, e.Pending)
	case 0x0A:
		return dtcResponse(sid, e.Permanent)
	case 0x04:
		e.Stored, e.Pending = nil, nil
		return []byte{0x44}
	case 0x09:
		if len(req) >= 2 && e.VIN != "" {
			switch req[1] {
			case 0x00:
				return []byte{0x49, 0x00, 0x40, 0x00, 0x00, 0x00} // PID 02 supported
			case 0x02:
				return append([]byte{0x49, 0x02, 0x01}, e.VIN...)
			}
		}
		return nil
	case 0x22:
		if len(req) >= 3 {
			did := uint16(req[1])<<8 | uint16(req[2])
			if d, ok := e.DIDs[did]; ok {
				return append([]byte{0x62, req[1], req[2]}, d...)
			}
		}
		return negative(sid, 0x31, isFunctional)
	default:
		return negative(sid, 0x11, isFunctional)
	}
}

// pidData returns the data for a service 01 PID, computing the "PIDs
// supported" bitmaps from the PIDs the ECU has.
func (e *ECU) pidData(p obd2.PID) ([]byte, bool) {
	if p%0x20 != 0 {
		d, ok := e.PIDs[p]
		return d, ok
	}
	// Bit n stands for PID base+n+1. The last bit, for the next bitmap PID,
	// tells the tester that PIDs beyond this range exist.
	var bitmap [4]byte
	base, found := int(p), false
	for q := range e.PIDs {
		switch qi := int(q); {
		case qi%0x20 == 0:
			continue // bitmap PIDs are derived, not configured
		case qi > base && qi < base+0x20:
			bit := qi - base - 1
			bitmap[bit/8] |= 0x80 >> (bit % 8)
			found = true
		case qi > base+0x20:
			bitmap[3] |= 0x01
			found = true
		}
	}
	if !found && p != 0 {
		return nil, false
	}
	return bitmap[:], true
}

func dtcResponse(sid byte, codes []obd2.DTC) []byte {
	resp := []byte{sid + 0x40, byte(len(codes))}
	for _, d := range slices.Clone(codes) {
		resp = append(resp, byte(d>>8), byte(d))
	}
	return resp
}

// negative returns a negative response, or nil where ISO 14229-1 says an ECU
// must not answer a functional request with that code.
func negative(sid, code byte, isFunctional bool) []byte {
	if isFunctional && (code == 0x11 || code == 0x12 || code == 0x31) {
		return nil
	}
	return []byte{0x7F, sid, code}
}
