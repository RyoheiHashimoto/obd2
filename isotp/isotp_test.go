package isotp

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// A VIN response (service 09, PID 02) as an engine ECU sends it: 20 bytes,
// so one first frame and two consecutive frames. The VIN is Wikipedia's
// example.
var (
	vinMsg = append([]byte{0x49, 0x02, 0x01}, "1M8GDM9AXKP042788"...)
	vinFF  = []byte{0x10, 0x14, 0x49, 0x02, 0x01, 0x31, 0x4D, 0x38}
	vinCF1 = []byte{0x21, 0x47, 0x44, 0x4D, 0x39, 0x41, 0x58, 0x4B}
	vinCF2 = []byte{0x22, 0x50, 0x30, 0x34, 0x32, 0x37, 0x38, 0x38}
)

func TestSTmin(t *testing.T) {
	tests := []struct {
		d    time.Duration
		b    byte
		back time.Duration
	}{
		{0, 0x00, 0},
		{time.Millisecond, 0x01, time.Millisecond},
		{127 * time.Millisecond, 0x7F, 127 * time.Millisecond},
		{time.Second, 0x7F, 127 * time.Millisecond},
		{100 * time.Microsecond, 0xF1, 100 * time.Microsecond},
		{900 * time.Microsecond, 0xF9, 900 * time.Microsecond},
		{50 * time.Microsecond, 0xF1, 100 * time.Microsecond},
	}
	for _, tt := range tests {
		if got := encodeSTmin(tt.d); got != tt.b {
			t.Errorf("encodeSTmin(%v) = %#x, want %#x", tt.d, got, tt.b)
		}
		if got := decodeSTmin(tt.b); got != tt.back {
			t.Errorf("decodeSTmin(%#x) = %v, want %v", tt.b, got, tt.back)
		}
	}
	for _, b := range []byte{0x80, 0xF0, 0xFA, 0xFF} {
		if got := decodeSTmin(b); got != 127*time.Millisecond {
			t.Errorf("reserved decodeSTmin(%#x) = %v, want 127ms", b, got)
		}
	}
}

func TestOptionsFramePadding(t *testing.T) {
	o := Options{Padding: 0xAA}
	f := o.Frame(0x7E0, []byte{0x02, 0x01, 0x0C})
	if f.Len != 8 || !bytes.Equal(f.Payload(), []byte{0x02, 0x01, 0x0C, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA}) {
		t.Errorf("padded frame = %v", f)
	}
	o = Options{NoPadding: true, Extended: true}
	f = o.Frame(0x18DB33F1, []byte{0x02, 0x01, 0x0C})
	if f.Len != 3 || !f.Extended {
		t.Errorf("unpadded frame = %v (extended %v)", f, f.Extended)
	}
}

func TestReceiverSingleFrame(t *testing.T) {
	r := NewReceiver(Options{})
	fc, msg, err := r.Feed([]byte{0x03, 0x41, 0x0D, 0x32, 0, 0, 0, 0})
	if err != nil || fc != nil || !bytes.Equal(msg, []byte{0x41, 0x0D, 0x32}) {
		t.Fatalf("Feed = %x, %x, %v", fc, msg, err)
	}
}

func TestReceiverMultiFrame(t *testing.T) {
	r := NewReceiver(Options{})
	fc, msg, err := r.Feed(vinFF)
	if err != nil || msg != nil || !bytes.Equal(fc, []byte{0x30, 0x00, 0x00}) {
		t.Fatalf("first frame: fc %x, msg %x, err %v", fc, msg, err)
	}
	if !r.Active() {
		t.Fatal("not active after first frame")
	}
	if fc, msg, err = r.Feed(vinCF1); err != nil || fc != nil || msg != nil {
		t.Fatalf("CF1: fc %x, msg %x, err %v", fc, msg, err)
	}
	if _, msg, err = r.Feed(vinCF2); err != nil || !bytes.Equal(msg, vinMsg) {
		t.Fatalf("CF2: msg %x, err %v", msg, err)
	}
	if r.Active() {
		t.Error("still active after the last frame")
	}
}

func TestReceiverBlockSize(t *testing.T) {
	r := NewReceiver(Options{BlockSize: 2, STmin: 5 * time.Millisecond})
	msg := seqBytes(30) // first frame + 4 consecutive frames (7, 7, 7, 3)
	fc, _, _ := r.Feed(append([]byte{0x10, 30}, msg[:6]...))
	if !bytes.Equal(fc, []byte{0x30, 0x02, 0x05}) {
		t.Fatalf("flow control = %x", fc)
	}
	var got []byte
	for i, off := 0, 6; off < 30; i++ {
		end := min(off+7, 30)
		fc, m, err := r.Feed(append([]byte{0x20 | byte(i+1)}, msg[off:end]...))
		if err != nil {
			t.Fatal(err)
		}
		wantFC := i == 1 // after the second frame of the block, and not after the last
		if (fc != nil) != wantFC {
			t.Errorf("CF %d: flow control %x, want one: %v", i+1, fc, wantFC)
		}
		got = m
		off = end
	}
	if !bytes.Equal(got, msg) {
		t.Errorf("message = %x, want %x", got, msg)
	}
}

func TestReceiverErrors(t *testing.T) {
	r := NewReceiver(Options{})

	if _, _, err := r.Feed(vinFF); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Feed(vinCF2); !errors.Is(err, ErrSequence) {
		t.Errorf("out-of-order CF: err = %v", err)
	}
	if r.Active() {
		t.Error("active after a sequence error")
	}

	for _, p := range [][]byte{{0x00, 1, 2}, {0x08, 1, 2, 3, 4, 5, 6, 7}, {0x05, 1, 2}} {
		if _, _, err := r.Feed(p); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("single frame %x: err = %v", p, err)
		}
	}

	fc, _, err := r.Feed([]byte{0x10, 0x00, 0, 0, 0, 0, 0, 0})
	if !errors.Is(err, ErrTooLong) || !bytes.Equal(fc, []byte{0x32, 0, 0}) {
		t.Errorf("escape first frame: fc %x, err %v", fc, err)
	}
}

func TestReceiverIgnoresStrayFrames(t *testing.T) {
	r := NewReceiver(Options{})
	for _, p := range [][]byte{
		{0x21, 1, 2, 3, 4, 5, 6, 7},    // consecutive frame with no first frame
		{0x30, 0, 0},                   // flow control
		{0x10, 0x05, 1, 2, 3, 4, 5, 6}, // first frame announcing < 8 bytes
	} {
		fc, msg, err := r.Feed(p)
		if fc != nil || msg != nil || err != nil || r.Active() {
			t.Errorf("Feed(%x) = %x, %x, %v, active %v", p, fc, msg, err, r.Active())
		}
	}
}

func TestReceiverSingleFrameAbortsTransfer(t *testing.T) {
	r := NewReceiver(Options{})
	if _, _, err := r.Feed(vinFF); err != nil {
		t.Fatal(err)
	}
	_, msg, err := r.Feed([]byte{0x02, 0x41, 0x00})
	if err != nil || !bytes.Equal(msg, []byte{0x41, 0x00}) || r.Active() {
		t.Errorf("msg %x, err %v, active %v", msg, err, r.Active())
	}
}

func seqBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}
