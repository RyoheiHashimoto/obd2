package obd2

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/isotp"
)

// Addressing selects the CAN identifiers of ISO 15765-4.
type Addressing int

const (
	// AddressingAuto uses 11-bit identifiers, falls back to 29-bit ones if
	// no ECU answers, and keeps whichever worked.
	AddressingAuto Addressing = iota
	// Addressing11Bit broadcasts on 0x7DF; ECUs answer on 0x7E8-0x7EF.
	Addressing11Bit
	// Addressing29Bit broadcasts on 0x18DB33F1; ECUs answer on 0x18DAF1xx.
	Addressing29Bit
)

// CANOptions configures a CANTransport. The zero value broadcasts requests
// and detects the addressing.
type CANOptions struct {
	// Addressing selects 11-bit or 29-bit identifiers.
	Addressing Addressing
	// ECU, when nonzero, sends requests to one ECU instead of all of them.
	// With 11-bit identifiers it is the ECU's request identifier (0x7E0 for
	// the engine ECU); with 29-bit identifiers it is the ECU's address byte
	// (0x10 for the engine ECU). A directed request returns as soon as the
	// ECU answers, so it is the faster choice for polling.
	ECU uint32
	// Timeout is how long to wait for answers. Zero means 100 ms.
	Timeout time.Duration
	// ISOTP tunes the transport layer. Its Extended field is ignored.
	ISOTP isotp.Options
}

// CANTransport sends OBD requests over a raw CAN bus as ISO 15765-4
// specifies. It is not safe for concurrent use; Client serializes requests.
type CANTransport struct {
	bus  can.Bus
	opt  CANOptions
	mode Addressing // AddressingAuto until an ECU has answered
}

var _ Transport = (*CANTransport)(nil)

// responsePendingTimeout is how long an ECU may take to answer after it has
// sent "response pending" (P2*CAN in ISO 15765-4).
const responsePendingTimeout = 5 * time.Second

// NewCANTransport returns a transport that sends requests on bus.
func NewCANTransport(bus can.Bus, opt CANOptions) *CANTransport {
	return &CANTransport{bus: bus, opt: opt, mode: opt.Addressing}
}

// RoundTrip implements Transport.
func (t *CANTransport) RoundTrip(ctx context.Context, req []byte) ([]Response, error) {
	if t.mode != AddressingAuto {
		return t.roundTrip(ctx, req, t.mode)
	}
	rs, err := t.roundTrip(ctx, req, Addressing11Bit)
	if err == nil {
		t.mode = Addressing11Bit
		return rs, nil
	}
	if !errors.Is(err, ErrNoResponse) {
		return nil, err
	}
	rs, err = t.roundTrip(ctx, req, Addressing29Bit)
	if err == nil {
		t.mode = Addressing29Bit
	}
	return rs, err
}

// ids returns the identifier to send requests on, and a function that
// reports whether a received identifier is an answer and, if so, the
// identifier to send its flow control frames on.
func (t *CANTransport) ids(mode Addressing) (req uint32, match func(id uint32) (fc uint32, ok bool)) {
	if mode == Addressing29Bit {
		if ecu := t.opt.ECU & 0xFF; t.opt.ECU != 0 {
			return 0x18DA00F1 | ecu<<8, func(id uint32) (uint32, bool) {
				return 0x18DA00F1 | ecu<<8, id == 0x18DAF100|ecu
			}
		}
		return 0x18DB33F1, func(id uint32) (uint32, bool) {
			if id&0x1FFFFF00 != 0x18DAF100 {
				return 0, false
			}
			return 0x18DA00F1 | (id&0xFF)<<8, true
		}
	}
	if ecu := t.opt.ECU; ecu != 0 {
		return ecu, func(id uint32) (uint32, bool) { return ecu, id == ecu+8 }
	}
	return 0x7DF, func(id uint32) (uint32, bool) {
		if id < 0x7E8 || id > 0x7EF {
			return 0, false
		}
		return id - 8, true
	}
}

type ecuState struct {
	rx       *isotp.Receiver
	deadline time.Time // while a message is incomplete or pending
}

func (t *CANTransport) roundTrip(ctx context.Context, req []byte, mode Addressing) ([]Response, error) {
	reqID, match := t.ids(mode)
	ext := mode == Addressing29Bit
	proto := ProtocolCAN11
	if ext {
		proto = ProtocolCAN29
	}
	opt := t.opt.ISOTP
	opt.Extended = ext
	directed := t.opt.ECU != 0

	switch {
	case len(req) == 0 || len(req) > isotp.MaxMessageLen:
		return nil, fmt.Errorf("obd2: request of %d bytes", len(req))
	case len(req) <= 7:
		if err := t.bus.Send(ctx, opt.Frame(reqID, append([]byte{byte(len(req))}, req...))); err != nil {
			return nil, err
		}
	case !directed:
		return nil, fmt.Errorf("obd2: a broadcast request must fit one frame (7 bytes), got %d", len(req))
	default:
		respID := t.opt.ECU + 8
		if ext {
			respID = 0x18DAF100 | t.opt.ECU&0xFF
		}
		if err := isotp.NewConn(t.bus, reqID, respID, opt).Send(ctx, req); err != nil {
			return nil, err
		}
	}

	timeout := t.opt.Timeout
	if timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	cfTimeout := opt.Timeout
	if cfTimeout <= 0 {
		cfTimeout = time.Second
	}
	window := time.Now().Add(timeout)
	ecus := map[uint32]*ecuState{}
	var out []Response
	for {
		deadline := window
		for _, s := range ecus {
			if s.deadline.After(deadline) {
				deadline = s.deadline
			}
		}
		rctx, cancel := context.WithDeadline(ctx, deadline)
		f, err := t.bus.Receive(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			return nil, err
		}
		if f.Extended != ext {
			continue
		}
		fcID, ok := match(f.ID)
		if !ok {
			continue
		}
		s := ecus[f.ID]
		if s == nil {
			s = &ecuState{rx: isotp.NewReceiver(opt)}
			ecus[f.ID] = s
		}
		fc, msg, err := s.rx.Feed(f.Payload())
		if fc != nil {
			if err := t.bus.Send(ctx, opt.Frame(fcID, fc)); err != nil {
				return nil, err
			}
		}
		if err != nil {
			return nil, fmt.Errorf("obd2: ECU %X: %w", f.ID, err)
		}
		s.deadline = time.Time{}
		if s.rx.Active() {
			s.deadline = time.Now().Add(cfTimeout)
		}
		if msg == nil {
			continue
		}
		if !answers(req, msg) {
			continue // an answer to another tester's request
		}
		if len(msg) >= 3 && msg[0] == 0x7F && msg[2] == nrcResponsePending {
			s.deadline = time.Now().Add(responsePendingTimeout)
			continue
		}
		out = append(out, Response{ECU: f.ID, Protocol: proto, Data: msg})
		if directed {
			return out, nil
		}
	}
	if len(out) == 0 {
		return nil, ErrNoResponse
	}
	return out, nil
}
