//go:build !linux

package socketcan

import (
	"context"
	"errors"

	"github.com/RyoheiHashimoto/obd2/can"
)

var errUnsupported = errors.New("socketcan: SocketCAN is only available on Linux")

// Conn is a raw CAN socket. SocketCAN exists only on Linux, so on this
// system it cannot be opened.
type Conn struct{}

var _ can.Bus = (*Conn)(nil)

// Open returns an error: SocketCAN is only available on Linux.
func Open(ifname string) (*Conn, error) { return nil, errUnsupported }

// SetFilter returns an error on this system.
func (c *Conn) SetFilter(filters ...Filter) error { return errUnsupported }

// Receive returns an error on this system.
func (c *Conn) Receive(ctx context.Context) (can.Frame, error) { return can.Frame{}, errUnsupported }

// Send returns an error on this system.
func (c *Conn) Send(ctx context.Context, f can.Frame) error { return errUnsupported }

// Close does nothing on this system.
func (c *Conn) Close() error { return nil }
