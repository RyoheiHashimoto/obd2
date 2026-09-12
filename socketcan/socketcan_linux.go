//go:build linux

package socketcan

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/RyoheiHashimoto/obd2/can"
)

// frameSize is the size of struct can_frame.
const frameSize = 16

// Conn is a raw CAN socket bound to one interface. It implements can.Bus.
// One goroutine may Receive while another Sends.
type Conn struct {
	f  *os.File
	rc syscall.RawConn
}

var _ can.Bus = (*Conn)(nil)

// Open opens a raw CAN socket on the named interface, such as "can0".
func Open(ifname string) (*Conn, error) {
	fd, err := unix.Socket(unix.AF_CAN, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, unix.CAN_RAW)
	if err != nil {
		return nil, fmt.Errorf("socketcan: socket: %w", err)
	}
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("socketcan: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrCAN{Ifindex: ifi.Index}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("socketcan: bind %s: %w", ifname, err)
	}
	// A non-blocking descriptor lets os.File use the runtime poller, so
	// deadlines and cancellation work.
	f := os.NewFile(uintptr(fd), "socketcan:"+ifname)
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("socketcan: %w", err)
	}
	return &Conn{f: f, rc: rc}, nil
}

// SetFilter makes the kernel deliver only frames that match one of the
// filters. With no filters the socket receives nothing.
func (c *Conn) SetFilter(filters ...Filter) error {
	fs := make([]unix.CanFilter, len(filters))
	for i, f := range filters {
		id, mask := f.ID&unix.CAN_SFF_MASK, f.Mask&unix.CAN_SFF_MASK
		if f.Extended {
			id, mask = f.ID&unix.CAN_EFF_MASK|unix.CAN_EFF_FLAG, f.Mask&unix.CAN_EFF_MASK
		}
		// Including the EFF flag in the mask makes the identifier length
		// part of the match.
		fs[i] = unix.CanFilter{Id: id, Mask: mask | unix.CAN_EFF_FLAG | unix.CAN_RTR_FLAG}
	}
	var serr error
	err := c.rc.Control(func(fd uintptr) {
		serr = unix.SetsockoptCanRawFilter(int(fd), unix.SOL_CAN_RAW, unix.CAN_RAW_FILTER, fs)
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		return fmt.Errorf("socketcan: set filter: %w", err)
	}
	return nil
}

// Receive returns the next data frame. Remote frames are skipped.
func (c *Conn) Receive(ctx context.Context) (can.Frame, error) {
	stop, err := watch(ctx, c.f.SetReadDeadline)
	if err != nil {
		return can.Frame{}, err
	}
	defer stop()
	var buf [frameSize]byte
	for {
		n, err := c.f.Read(buf[:])
		if err != nil {
			return can.Frame{}, ioError(ctx, err)
		}
		if n != frameSize {
			return can.Frame{}, fmt.Errorf("socketcan: read %d bytes, want %d", n, frameSize)
		}
		raw := binary.NativeEndian.Uint32(buf[0:4])
		if raw&(unix.CAN_RTR_FLAG|unix.CAN_ERR_FLAG) != 0 {
			continue
		}
		f := can.Frame{Len: min(buf[4], can.MaxDataLen)}
		if raw&unix.CAN_EFF_FLAG != 0 {
			f.ID, f.Extended = raw&unix.CAN_EFF_MASK, true
		} else {
			f.ID = raw & unix.CAN_SFF_MASK
		}
		copy(f.Data[:], buf[8:8+f.Len])
		return f, nil
	}
}

// Send transmits f. If the interface's transmit queue is full, it retries
// until ctx is done.
func (c *Conn) Send(ctx context.Context, f can.Frame) error {
	if f.Len > can.MaxDataLen {
		return fmt.Errorf("socketcan: frame length %d exceeds %d", f.Len, can.MaxDataLen)
	}
	var buf [frameSize]byte
	id := f.ID & unix.CAN_SFF_MASK
	if f.Extended {
		id = f.ID&unix.CAN_EFF_MASK | unix.CAN_EFF_FLAG
	}
	binary.NativeEndian.PutUint32(buf[0:4], id)
	buf[4] = f.Len
	copy(buf[8:], f.Payload())

	stop, err := watch(ctx, c.f.SetWriteDeadline)
	if err != nil {
		return err
	}
	defer stop()
	for {
		_, err := c.f.Write(buf[:])
		if !errors.Is(err, unix.ENOBUFS) {
			if err != nil {
				return ioError(ctx, err)
			}
			return nil
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close closes the socket. A Receive blocked on it returns can.ErrClosed.
func (c *Conn) Close() error {
	return c.f.Close()
}

// watch applies ctx's deadline to one direction of the file, and interrupts
// the I/O when ctx is canceled. Call the returned function when the I/O is
// over.
func watch(ctx context.Context, setDeadline func(time.Time) error) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline() // the zero time means none
	if err := setDeadline(deadline); err != nil {
		return nil, ioError(ctx, err)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = setDeadline(time.Unix(1, 0))
		close(done)
	})
	return func() {
		if !stop() {
			<-done // keep the interruption from hitting the next call
		}
	}, nil
}

func ioError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, os.ErrDeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, os.ErrClosed):
		return can.ErrClosed
	}
	return fmt.Errorf("socketcan: %w", err)
}
