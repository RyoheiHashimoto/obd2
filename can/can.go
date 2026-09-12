// Package can defines a classic CAN frame and the Bus interface through which
// the other packages in this module send and receive frames.
package can

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MaxDataLen is the payload size of a classic CAN frame.
const MaxDataLen = 8

// ErrClosed is returned by operations on a closed endpoint.
var ErrClosed = errors.New("can: endpoint closed")

// Frame is a classic CAN 2.0 data frame.
type Frame struct {
	ID       uint32 // 11-bit identifier, or 29-bit when Extended is set
	Extended bool
	Len      uint8 // number of valid bytes in Data, 0-8
	Data     [MaxDataLen]byte
}

// Payload returns the valid part of Data.
func (f Frame) Payload() []byte {
	return f.Data[:min(f.Len, MaxDataLen)]
}

// String formats the frame the way candump and cansend do, e.g. "7E8#03410D32".
func (f Frame) String() string {
	var b strings.Builder
	if f.Extended {
		fmt.Fprintf(&b, "%08X#", f.ID)
	} else {
		fmt.Fprintf(&b, "%03X#", f.ID)
	}
	for _, c := range f.Payload() {
		fmt.Fprintf(&b, "%02X", c)
	}
	return b.String()
}

// Bus sends and receives CAN frames.
//
// A Bus value is one endpoint on the bus. Like a SocketCAN raw socket, it
// receives every frame the other endpoints send, but not the frames it sends
// itself. Implementations must allow one goroutine to call Receive while
// another calls Send.
type Bus interface {
	Send(ctx context.Context, f Frame) error
	Receive(ctx context.Context) (Frame, error)
}
