package isotp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RyoheiHashimoto/obd2/can"
)

// Conn is an ISO-TP endpoint with physical addressing: it sends frames with
// one CAN identifier and receives frames with another. For OBD, a tester
// talking to the engine ECU sends on 0x7E0 and receives on 0x7E8.
//
// A Conn is not safe for concurrent use.
type Conn struct {
	bus  can.Bus
	txID uint32
	rxID uint32
	opt  Options
}

// NewConn returns a Conn that sends with txID and receives with rxID.
func NewConn(bus can.Bus, txID, rxID uint32, opt Options) *Conn {
	return &Conn{bus: bus, txID: txID, rxID: rxID, opt: opt}
}

// Send transmits msg. A message longer than 7 bytes is split into a first
// frame and consecutive frames, paced by the block size and STmin that the
// peer requests in its flow control frames.
func (c *Conn) Send(ctx context.Context, msg []byte) error {
	n := len(msg)
	switch {
	case n == 0:
		return fmt.Errorf("%w: empty message", ErrInvalidFrame)
	case n > MaxMessageLen:
		return fmt.Errorf("%w: %d bytes", ErrTooLong, n)
	case n <= 7:
		return c.bus.Send(ctx, c.opt.Frame(c.txID, append([]byte{byte(n)}, msg...)))
	}

	ff := append([]byte{pciFirst<<4 | byte(n>>8), byte(n)}, msg[:6]...)
	if err := c.bus.Send(ctx, c.opt.Frame(c.txID, ff)); err != nil {
		return err
	}
	off, seq := 6, byte(1)
	for off < n {
		bs, st, err := c.waitFlowControl(ctx)
		if err != nil {
			return err
		}
		for i := 0; off < n && (bs == 0 || i < bs); i++ {
			if i > 0 && st > 0 {
				if err := sleep(ctx, st); err != nil {
					return err
				}
			}
			end := min(off+7, n)
			cf := append([]byte{pciConsecutive<<4 | seq}, msg[off:end]...)
			if err := c.bus.Send(ctx, c.opt.Frame(c.txID, cf)); err != nil {
				return err
			}
			off = end
			seq = (seq + 1) & 0x0F
		}
	}
	return nil
}

// waitFlowControl waits for a flow control frame that lets the transfer
// continue and returns the block size and STmin it carries.
func (c *Conn) waitFlowControl(ctx context.Context) (blockSize int, stmin time.Duration, err error) {
	waits := 0
	deadline := time.Now().Add(c.opt.timeout())
	for {
		f, err := c.receiveFromPeer(ctx, deadline)
		if err != nil {
			return 0, 0, timeoutError(ctx, err, "flow control")
		}
		p := f.Payload()
		if len(p) < 3 || p[0]>>4 != pciFlowControl {
			continue
		}
		switch p[0] & 0x0F {
		case flowContinue:
			return int(p[1]), decodeSTmin(p[2]), nil
		case flowWait:
			waits++
			if waits > c.opt.maxWaitFrames() {
				return 0, 0, fmt.Errorf("%w: peer sent %d WAIT frames in a row", ErrTimeout, waits)
			}
			deadline = time.Now().Add(c.opt.timeout())
		case flowOverflow:
			return 0, 0, ErrOverflow
		default:
			return 0, 0, fmt.Errorf("%w: flow status %#x", ErrInvalidFrame, p[0]&0x0F)
		}
	}
}

// Receive waits for the next complete message from the peer. The wait for a
// message to start is bounded only by ctx; once a first frame has arrived,
// each consecutive frame must follow within the timeout (N_Cr).
func (c *Conn) Receive(ctx context.Context) ([]byte, error) {
	r := NewReceiver(c.opt)
	var deadline time.Time
	for {
		f, err := c.receiveFromPeer(ctx, deadline)
		if err != nil {
			if r.Active() {
				return nil, timeoutError(ctx, err, "consecutive frame")
			}
			return nil, err
		}
		fc, msg, err := r.Feed(f.Payload())
		if fc != nil {
			if serr := c.bus.Send(ctx, c.opt.Frame(c.txID, fc)); serr != nil {
				return nil, serr
			}
		}
		if err != nil {
			return nil, err
		}
		if msg != nil {
			return msg, nil
		}
		deadline = time.Time{}
		if r.Active() {
			deadline = time.Now().Add(c.opt.timeout())
		}
	}
}

// receiveFromPeer returns the next frame carrying the peer's identifier. A
// zero deadline means no limit beyond ctx.
func (c *Conn) receiveFromPeer(ctx context.Context, deadline time.Time) (can.Frame, error) {
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	for {
		f, err := c.bus.Receive(ctx)
		if err != nil {
			return can.Frame{}, err
		}
		if f.ID == c.rxID && f.Extended == c.opt.Extended {
			return f, nil
		}
	}
}

// timeoutError reports an expired protocol timer as ErrTimeout, while the
// caller's own cancellation or deadline is returned unchanged.
func timeoutError(ctx context.Context, err error, waitingFor string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w waiting for %s", ErrTimeout, waitingFor)
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
