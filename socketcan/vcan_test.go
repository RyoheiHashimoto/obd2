//go:build linux && vcan

// These tests run against a real SocketCAN stack. They need an interface
// named vcan0, and the kernel cross-check also needs the can-isotp module
// and can-utils:
//
//	sudo modprobe vcan can-isotp
//	sudo ip link add dev vcan0 type vcan && sudo ip link set up vcan0
//	go test -tags vcan ./socketcan/
package socketcan_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/can"
	"github.com/RyoheiHashimoto/obd2/ecusim"
	"github.com/RyoheiHashimoto/obd2/isotp"
	"github.com/RyoheiHashimoto/obd2/socketcan"
)

const iface = "vcan0"

func open(t *testing.T) *socketcan.Conn {
	t.Helper()
	c, err := socketcan.Open(iface)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func testCtx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestFrameRoundTrip(t *testing.T) {
	a, b := open(t), open(t)
	ctx := testCtx(t)
	for _, f := range []can.Frame{
		{ID: 0x7DF, Len: 8, Data: [8]byte{0x02, 0x01, 0x0C}},
		{ID: 0x18DB33F1, Extended: true, Len: 3, Data: [8]byte{0x02, 0x01, 0x0D}},
		{ID: 0x123},
	} {
		if err := a.Send(ctx, f); err != nil {
			t.Fatal(err)
		}
		got, err := b.Receive(ctx)
		if err != nil || got != f {
			t.Errorf("sent %v, received %v (extended %v), %v", f, got, got.Extended, err)
		}
	}
}

func TestReceiveHonorsContext(t *testing.T) {
	c := open(t)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline: err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := c.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: err = %v", err)
	}
	// The interruption must not leak into the next call.
	a := open(t)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = a.Send(context.Background(), can.Frame{ID: 0x7E8, Len: 1})
	}()
	if f, err := c.Receive(testCtx(t)); err != nil || f.ID != 0x7E8 {
		t.Errorf("after cancel: %v, %v", f, err)
	}
}

func TestCloseUnblocksReceive(t *testing.T) {
	c, err := socketcan.Open(iface)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Receive(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, can.ErrClosed) {
			t.Errorf("err = %v, want can.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Receive did not return after Close")
	}
}

func TestFilter(t *testing.T) {
	a, b := open(t), open(t)
	if err := b.SetFilter(socketcan.Filter{ID: 0x7E8, Mask: 0x7F8}); err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	for _, id := range []uint32{0x201, 0x7DF, 0x7E9} {
		_ = a.Send(ctx, can.Frame{ID: id, Len: 1})
	}
	if f, err := b.Receive(ctx); err != nil || f.ID != 0x7E9 {
		t.Errorf("first frame through the filter = %v, %v; want 7E9", f, err)
	}
}

func TestOBDOverSocketCAN(t *testing.T) {
	ctx := testCtx(t)
	engine := &ecusim.ECU{
		Address: 0x7E0,
		PIDs:    map[obd2.PID][]byte{obd2.EngineRPM: {0x1A, 0xF8}},
		Stored:  []obd2.DTC{0x0133},
		VIN:     "1M8GDM9AXKP042788",
	}
	ecuConn := open(t)
	go func() { _ = engine.Serve(ctx, ecuConn) }()
	c := obd2.NewClient(obd2.NewCANTransport(open(t), obd2.CANOptions{}))

	if v, err := c.Query(ctx, obd2.EngineRPM); err != nil {
		t.Error(err)
	} else if rpm, _ := v.Float(obd2.EngineRPM); rpm != 1726 {
		t.Errorf("rpm = %v", rpm)
	}
	if vin, err := c.VIN(ctx); err != nil || vin != engine.VIN {
		t.Errorf("VIN = %q, %v", vin, err)
	}
	if codes, err := c.StoredDTCs(ctx); err != nil || len(codes) != 1 || codes[0] != 0x0133 {
		t.Errorf("StoredDTCs = %v, %v", codes, err)
	}
}

// TestKernelISOTP checks this package's ISO-TP against the Linux kernel's
// implementation, in both directions, using can-utils.
func TestKernelISOTP(t *testing.T) {
	for _, tool := range []string{"isotpsend", "isotprecv"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	if b, _ := os.ReadFile("/proc/net/protocols"); !bytes.Contains(b, []byte("CAN_ISOTP")) {
		t.Skip("can-isotp module not loaded")
	}
	msg := make([]byte, 300) // many consecutive frames; the sequence number wraps
	for i := range msg {
		msg[i] = byte(i * 7)
	}
	hexMsg := strings.ToUpper(strings.TrimSpace(fmt.Sprintf("% x", msg)))

	t.Run("kernel sends", func(t *testing.T) {
		for _, opt := range []isotp.Options{{}, {BlockSize: 2, STmin: time.Millisecond}} {
			conn := isotp.NewConn(open(t), 0x7E0, 0x7E8, opt)
			got := make(chan []byte, 1)
			go func() {
				m, err := conn.Receive(testCtx(t))
				if err != nil {
					t.Error(err)
				}
				got <- m
			}()
			time.Sleep(50 * time.Millisecond)
			cmd := exec.Command("isotpsend", "-s", "7E8", "-d", "7E0", iface)
			cmd.Stdin = strings.NewReader(hexMsg + "\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isotpsend: %v: %s", err, out)
			}
			if m := <-got; !bytes.Equal(m, msg) {
				t.Errorf("%+v: received %d bytes that differ from what the kernel sent", opt, len(m))
			}
		}
	})

	t.Run("kernel receives", func(t *testing.T) {
		for _, args := range [][]string{{}, {"-b", "3", "-m", "2"}} {
			cmd := exec.Command("isotprecv", append(append([]string{"-s", "7E8", "-d", "7E0"}, args...), iface)...)
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond) // let it bind
			if err := isotp.NewConn(open(t), 0x7E0, 0x7E8, isotp.Options{}).Send(testCtx(t), msg); err != nil {
				_ = cmd.Process.Kill()
				t.Fatalf("%v: %v", args, err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("isotprecv: %v", err)
			}
			got, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(out.String()), " ", ""))
			if err != nil || !bytes.Equal(got, msg) {
				t.Errorf("%v: kernel received %q", args, out.String())
			}
		}
	})
}
