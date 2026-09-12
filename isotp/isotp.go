// Package isotp implements the ISO 15765-2 transport protocol (ISO-TP) over
// classic CAN.
//
// It runs in user space on top of a can.Bus, so it needs no kernel module and
// works the same on Linux SocketCAN, on a simulated bus, or on any other Bus
// implementation.
package isotp

import (
	"errors"
	"fmt"
	"time"

	"github.com/RyoheiHashimoto/obd2/can"
)

// MaxMessageLen is the longest message a classic CAN first frame can announce.
const MaxMessageLen = 4095

// Protocol control information types, the high nibble of the first byte.
const (
	pciSingle      = 0x0
	pciFirst       = 0x1
	pciConsecutive = 0x2
	pciFlowControl = 0x3
)

// Flow status values carried by a flow control frame.
const (
	flowContinue = 0x0
	flowWait     = 0x1
	flowOverflow = 0x2
)

var (
	// ErrTimeout means the peer did not send a flow control frame (N_Bs) or
	// the next consecutive frame (N_Cr) in time.
	ErrTimeout = errors.New("isotp: timeout")
	// ErrOverflow means the receiver rejected the message as too long.
	ErrOverflow = errors.New("isotp: receiver reported overflow")
	// ErrSequence means a consecutive frame arrived out of order.
	ErrSequence = errors.New("isotp: wrong sequence number")
	// ErrInvalidFrame means a frame violated the protocol.
	ErrInvalidFrame = errors.New("isotp: invalid frame")
	// ErrTooLong means a message exceeds MaxMessageLen.
	ErrTooLong = errors.New("isotp: message too long")
)

// Options configures an ISO-TP endpoint. The zero value is usable and
// matches what ISO 15765-4 (OBD on CAN) expects: 11-bit identifiers and
// frames padded to 8 bytes.
type Options struct {
	// Extended selects 29-bit CAN identifiers.
	Extended bool
	// Padding is the byte that fills frames up to 8 bytes.
	Padding byte
	// NoPadding sends frames only as long as their content. ISO 15765-4
	// requires padding, so leave this off for OBD.
	NoPadding bool
	// BlockSize is the number of consecutive frames the peer may send before
	// it must wait for another flow control frame. 0 means no limit.
	BlockSize uint8
	// STmin is the minimum gap the peer must leave between consecutive
	// frames. Values below 1 ms are rounded to 100 µs steps.
	STmin time.Duration
	// Timeout bounds the wait for a flow control frame (N_Bs) and for each
	// consecutive frame (N_Cr). Zero means 1 second.
	Timeout time.Duration
	// MaxWaitFrames is how many flow control WAIT frames in a row are
	// accepted before giving up (N_WFTmax). Zero means 10.
	MaxWaitFrames int
}

func (o *Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return time.Second
}

func (o *Options) maxWaitFrames() int {
	if o.MaxWaitFrames > 0 {
		return o.MaxWaitFrames
	}
	return 10
}

// Frame builds a CAN frame with the given identifier and payload, padded
// according to the options. The payload must not exceed 8 bytes.
func (o *Options) Frame(id uint32, payload []byte) can.Frame {
	f := can.Frame{ID: id, Extended: o.Extended}
	n := copy(f.Data[:], payload)
	f.Len = uint8(n)
	if !o.NoPadding {
		for i := n; i < can.MaxDataLen; i++ {
			f.Data[i] = o.Padding
		}
		f.Len = can.MaxDataLen
	}
	return f
}

// encodeSTmin converts a duration to the STmin byte of a flow control frame.
func encodeSTmin(d time.Duration) byte {
	switch {
	case d <= 0:
		return 0
	case d < time.Millisecond:
		steps := max(d/(100*time.Microsecond), 1)
		return 0xF0 + byte(steps)
	case d <= 127*time.Millisecond:
		return byte(d / time.Millisecond)
	default:
		return 0x7F
	}
}

// decodeSTmin converts an STmin byte to a duration. Reserved values are
// treated as the maximum, 127 ms, as ISO 15765-2 requires.
func decodeSTmin(b byte) time.Duration {
	switch {
	case b <= 0x7F:
		return time.Duration(b) * time.Millisecond
	case b >= 0xF1 && b <= 0xF9:
		return time.Duration(b-0xF0) * 100 * time.Microsecond
	default:
		return 127 * time.Millisecond
	}
}

// Receiver reassembles messages from the frames of a single sender.
//
// It is a pure state machine with no I/O and no timers: the caller reads
// frames, passes their payloads to Feed, transmits the flow control frames
// Feed returns, and enforces the N_Cr timeout while Active reports true.
// Conn uses one Receiver; a client that talks to several ECUs at once can
// keep one Receiver per ECU.
type Receiver struct {
	opt     Options
	active  bool
	want    int
	buf     []byte
	seq     byte
	inBlock int
}

// NewReceiver returns a Receiver that advertises the block size and STmin
// from opt in its flow control frames.
func NewReceiver(opt Options) *Receiver {
	return &Receiver{opt: opt}
}

// Active reports whether a multi-frame message is partly received.
func (r *Receiver) Active() bool { return r.active }

// Reset abandons any message in progress.
func (r *Receiver) Reset() {
	r.active = false
	r.want = 0
	r.buf = nil
	r.seq = 0
	r.inBlock = 0
}

// Feed processes the payload of one frame from the sender.
//
// It returns the payload of a flow control frame to send back, or nil, and
// the message once it is complete. Frames that do not belong to a transfer,
// such as flow control frames or consecutive frames with no first frame
// before them, are ignored. After an error the Receiver is reset.
func (r *Receiver) Feed(p []byte) (fc, msg []byte, err error) {
	if len(p) == 0 {
		return nil, nil, nil
	}
	switch p[0] >> 4 {
	case pciSingle:
		// A single frame aborts any transfer in progress (ISO 15765-2,
		// unexpected N_PDU handling).
		r.Reset()
		n := int(p[0] & 0x0F)
		if n == 0 || n > 7 || n > len(p)-1 {
			return nil, nil, fmt.Errorf("%w: single frame length %d in %d bytes", ErrInvalidFrame, n, len(p))
		}
		return nil, append([]byte(nil), p[1:1+n]...), nil

	case pciFirst:
		r.Reset()
		if len(p) < can.MaxDataLen {
			return nil, nil, fmt.Errorf("%w: first frame of %d bytes", ErrInvalidFrame, len(p))
		}
		n := int(p[0]&0x0F)<<8 | int(p[1])
		if n == 0 {
			// Escape sequence for 32-bit lengths, only defined for CAN FD.
			return []byte{pciFlowControl<<4 | flowOverflow, 0, 0}, nil,
				fmt.Errorf("%w: first frame announces a length above %d", ErrTooLong, MaxMessageLen)
		}
		if n < can.MaxDataLen {
			// Such a message fits a single frame; ISO 15765-2 says to
			// ignore the first frame.
			return nil, nil, nil
		}
		r.active = true
		r.want = n
		r.buf = make([]byte, 0, n)
		r.buf = append(r.buf, p[2:8]...)
		r.seq = 1
		return r.flowControl(), nil, nil

	case pciConsecutive:
		if !r.active {
			return nil, nil, nil
		}
		if got := p[0] & 0x0F; got != r.seq {
			r.Reset()
			return nil, nil, fmt.Errorf("%w: got %d, want %d", ErrSequence, got, r.seq)
		}
		take := min(r.want-len(r.buf), len(p)-1)
		r.buf = append(r.buf, p[1:1+take]...)
		r.seq = (r.seq + 1) & 0x0F
		if len(r.buf) >= r.want {
			msg := r.buf
			r.Reset()
			return nil, msg, nil
		}
		if r.opt.BlockSize > 0 {
			r.inBlock++
			if r.inBlock == int(r.opt.BlockSize) {
				r.inBlock = 0
				return r.flowControl(), nil, nil
			}
		}
		return nil, nil, nil

	default:
		return nil, nil, nil
	}
}

func (r *Receiver) flowControl() []byte {
	return []byte{pciFlowControl<<4 | flowContinue, r.opt.BlockSize, encodeSTmin(r.opt.STmin)}
}
