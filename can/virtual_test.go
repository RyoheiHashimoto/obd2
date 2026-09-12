package can

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVirtualBusDeliversToOthersOnly(t *testing.T) {
	bus := NewVirtualBus()
	a, b, c := bus.Connect(), bus.Connect(), bus.Connect()
	ctx := context.Background()

	f := Frame{ID: 0x7DF, Len: 3, Data: [8]byte{0x02, 0x01, 0x0D}}
	if err := a.Send(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*VirtualPort{b, c} {
		got, err := p.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != f {
			t.Errorf("got %v, want %v", got, f)
		}
	}

	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := a.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("sender received its own frame: err = %v", err)
	}
}

func TestVirtualBusKeepsOrder(t *testing.T) {
	bus := NewVirtualBus()
	a, b := bus.Connect(), bus.Connect()
	ctx := context.Background()
	for i := range 100 {
		if err := a.Send(ctx, Frame{ID: uint32(i), Len: 0}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 100 {
		f, err := b.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.ID != uint32(i) {
			t.Fatalf("frame %d has ID %d", i, f.ID)
		}
	}
}

func TestVirtualPortCloseUnblocksReceive(t *testing.T) {
	bus := NewVirtualBus()
	a := bus.Connect()
	done := make(chan error, 1)
	go func() {
		_, err := a.Receive(context.Background())
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Receive did not return after Close")
	}
	if err := a.Send(context.Background(), Frame{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close: err = %v, want ErrClosed", err)
	}
}

func TestFrameString(t *testing.T) {
	tests := []struct {
		f    Frame
		want string
	}{
		{Frame{ID: 0x7E8, Len: 4, Data: [8]byte{0x03, 0x41, 0x0D, 0x32}}, "7E8#03410D32"},
		{Frame{ID: 0x18DAF110, Extended: true, Len: 2, Data: [8]byte{0x01, 0xFF}}, "18DAF110#01FF"},
		{Frame{ID: 0x123}, "123#"},
	}
	for _, tt := range tests {
		if got := tt.f.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
