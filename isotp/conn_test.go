package isotp

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2/can"
)

// connPair returns a tester Conn and an ECU Conn on one virtual bus.
func connPair(t *testing.T, tester, ecu Options) (*Conn, *Conn) {
	t.Helper()
	bus := can.NewVirtualBus()
	a, b := bus.Connect(), bus.Connect()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return NewConn(a, 0x7E0, 0x7E8, tester), NewConn(b, 0x7E8, 0x7E0, ecu)
}

// rawPeer returns a tester Conn and a raw endpoint on which the test plays
// the ECU frame by frame.
func rawPeer(t *testing.T, opt Options) (*Conn, *can.VirtualPort) {
	t.Helper()
	bus := can.NewVirtualBus()
	a, b := bus.Connect(), bus.Connect()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return NewConn(a, 0x7E0, 0x7E8, opt), b
}

func recvFrame(t *testing.T, p *can.VirtualPort) can.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, err := p.Receive(ctx)
	if err != nil {
		t.Fatalf("peer receive: %v", err)
	}
	return f
}

func sendFrame(t *testing.T, p *can.VirtualPort, id uint32, data ...byte) {
	t.Helper()
	f := can.Frame{ID: id, Len: uint8(len(data))}
	copy(f.Data[:], data)
	if err := p.Send(context.Background(), f); err != nil {
		t.Fatalf("peer send: %v", err)
	}
}

func startSend(c *Conn, msg []byte) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- c.Send(context.Background(), msg) }()
	return errc
}

func TestConnRoundTrip(t *testing.T) {
	receivers := []Options{
		{},
		{BlockSize: 1},
		{BlockSize: 3, STmin: 100 * time.Microsecond},
		{NoPadding: true},
	}
	for _, n := range []int{1, 7, 8, 13, 20, 111, 112, 113, 500, MaxMessageLen} {
		for _, ro := range receivers {
			tester, ecu := connPair(t, ro, Options{NoPadding: ro.NoPadding})
			msg := seqBytes(n)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			errc := make(chan error, 1)
			go func() { errc <- ecu.Send(ctx, msg) }()
			got, rerr := tester.Receive(ctx)
			serr := <-errc
			cancel()
			if rerr != nil || serr != nil {
				t.Fatalf("%d bytes, %+v: receive %v, send %v", n, ro, rerr, serr)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("%d bytes, %+v: got %d bytes that differ", n, ro, len(got))
			}
		}
	}
}

func TestSendRejectsBadLengths(t *testing.T) {
	c, _ := rawPeer(t, Options{})
	if err := c.Send(context.Background(), nil); !errors.Is(err, ErrInvalidFrame) {
		t.Errorf("empty: err = %v", err)
	}
	if err := c.Send(context.Background(), make([]byte, MaxMessageLen+1)); !errors.Is(err, ErrTooLong) {
		t.Errorf("too long: err = %v", err)
	}
}

func TestSendHonorsWait(t *testing.T) {
	c, peer := rawPeer(t, Options{Timeout: 200 * time.Millisecond})
	msg := seqBytes(20)
	errc := startSend(c, msg)

	ff := recvFrame(t, peer)
	if ff.ID != 0x7E0 || ff.Len != 8 || ff.Data[0] != 0x10 || ff.Data[1] != 20 {
		t.Fatalf("first frame = %v", ff)
	}
	sendFrame(t, peer, 0x7E8, 0x31, 0, 0) // WAIT
	sendFrame(t, peer, 0x7E8, 0x31, 0, 0) // WAIT
	sendFrame(t, peer, 0x7E8, 0x30, 0, 0) // continue, no block limit
	got := append([]byte(nil), ff.Data[2:]...)
	for seq := byte(1); len(got) < len(msg); seq++ {
		cf := recvFrame(t, peer)
		if cf.Data[0] != 0x20|seq {
			t.Fatalf("consecutive frame %d has PCI %#x", seq, cf.Data[0])
		}
		got = append(got, cf.Data[1:]...)
	}
	if !bytes.Equal(got[:len(msg)], msg) {
		t.Errorf("peer got %x, want %x", got[:len(msg)], msg)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestSendGivesUpAfterTooManyWaits(t *testing.T) {
	c, peer := rawPeer(t, Options{MaxWaitFrames: 1, Timeout: 200 * time.Millisecond})
	errc := startSend(c, seqBytes(20))
	recvFrame(t, peer)
	sendFrame(t, peer, 0x7E8, 0x31, 0, 0)
	sendFrame(t, peer, 0x7E8, 0x31, 0, 0)
	if err := <-errc; !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestSendOverflow(t *testing.T) {
	c, peer := rawPeer(t, Options{})
	errc := startSend(c, seqBytes(20))
	recvFrame(t, peer)
	sendFrame(t, peer, 0x7E8, 0x32, 0, 0)
	if err := <-errc; !errors.Is(err, ErrOverflow) {
		t.Errorf("err = %v, want ErrOverflow", err)
	}
}

func TestSendTimesOutWithoutFlowControl(t *testing.T) {
	c, peer := rawPeer(t, Options{Timeout: 30 * time.Millisecond})
	errc := startSend(c, seqBytes(20))
	recvFrame(t, peer)
	if err := <-errc; !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestSendPacesBySTmin(t *testing.T) {
	c, peer := rawPeer(t, Options{})
	msg := seqBytes(6 + 7*5) // first frame and 5 consecutive frames
	start := time.Now()
	errc := startSend(c, msg)
	recvFrame(t, peer)
	sendFrame(t, peer, 0x7E8, 0x30, 0, 10) // STmin 10 ms
	for range 5 {
		recvFrame(t, peer)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 40*time.Millisecond {
		t.Errorf("5 consecutive frames took %v, want at least 4 gaps of 10ms", el)
	}
}

func TestReceiveSendsFlowControlThenTimesOut(t *testing.T) {
	c, peer := rawPeer(t, Options{BlockSize: 4, STmin: 2 * time.Millisecond, Timeout: 30 * time.Millisecond})
	errc := make(chan error, 1)
	go func() {
		_, err := c.Receive(context.Background())
		errc <- err
	}()
	sendFrame(t, peer, 0x7E8, vinFF...)
	fc := recvFrame(t, peer)
	if fc.ID != 0x7E0 || fc.Len != 8 || !bytes.Equal(fc.Payload()[:3], []byte{0x30, 4, 2}) {
		t.Errorf("flow control = %v", fc)
	}
	if err := <-errc; !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
}

func TestReceiveIgnoresOtherIDs(t *testing.T) {
	c, peer := rawPeer(t, Options{})
	sendFrame(t, peer, 0x201, 0x0B, 0xB8, 0x00, 0x32, 0, 0, 0x40, 0) // broadcast traffic
	sendFrame(t, peer, 0x7E9, 0x03, 0x41, 0x0D, 0x00)                // another ECU
	sendFrame(t, peer, 0x7E8, 0x03, 0x41, 0x0D, 0x32)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := c.Receive(ctx)
	if err != nil || !bytes.Equal(msg, []byte{0x41, 0x0D, 0x32}) {
		t.Errorf("Receive = %x, %v", msg, err)
	}
}

func TestReceiveReturnsCallerDeadline(t *testing.T) {
	c, _ := rawPeer(t, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Receive(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want the caller's context.DeadlineExceeded", err)
	}
}
