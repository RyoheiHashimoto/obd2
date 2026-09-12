//go:build linux && vcan

package socketcan_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/RyoheiHashimoto/obd2/isotp"
)

// TestPythonISOTP checks this module's ISO-TP against can-isotp, an
// independent implementation in Python, in both directions. It needs python3
// with the packages in testdata/requirements.txt.
func TestPythonISOTP(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	if err := exec.Command(python, "-c", "import can, isotp").Run(); err != nil {
		t.Skip("python-can and can-isotp not installed")
	}
	msg := make([]byte, 300) // many consecutive frames; the sequence number wraps
	for i := range msg {
		msg[i] = byte(i * 7)
	}

	t.Run("python sends", func(t *testing.T) {
		for _, opt := range []isotp.Options{{}, {BlockSize: 2, STmin: time.Millisecond}} {
			conn := isotp.NewConn(open(t), 0x7E0, 0x7E8, opt)
			got := make(chan []byte, 1)
			errc := make(chan error, 1)
			go func() {
				m, err := conn.Receive(testCtx(t))
				got <- m
				errc <- err
			}()
			cmd := exec.Command(python, "testdata/isotp_peer.py", "send", "7E8", "7E0")
			cmd.Stdin = strings.NewReader(hex.EncodeToString(msg))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%+v: peer: %v: %s", opt, err, out)
			}
			if err := <-errc; err != nil {
				t.Fatalf("%+v: %v", opt, err)
			}
			if m := <-got; !bytes.Equal(m, msg) {
				t.Errorf("%+v: received %d bytes that differ from what can-isotp sent", opt, len(m))
			}
		}
	})

	t.Run("python receives", func(t *testing.T) {
		for _, args := range [][]string{{}, {"--blocksize", "3", "--stmin", "2"}} {
			cmd := exec.Command(python, append([]string{"testdata/isotp_peer.py", "recv", "7E8", "7E0"}, args...)...)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines := bufio.NewScanner(stdout)
			if !lines.Scan() || lines.Text() != "ready" {
				_ = cmd.Process.Kill()
				t.Fatalf("%v: peer did not start: %s", args, stderr.String())
			}
			if err := isotp.NewConn(open(t), 0x7E0, 0x7E8, isotp.Options{}).Send(testCtx(t), msg); err != nil {
				_ = cmd.Process.Kill()
				t.Fatalf("%v: %v", args, err)
			}
			if !lines.Scan() {
				_ = cmd.Process.Kill()
				t.Fatalf("%v: peer printed nothing: %s", args, stderr.String())
			}
			line := lines.Text()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v: peer: %v: %s", args, err, stderr.String())
			}
			if got, err := hex.DecodeString(line); err != nil || !bytes.Equal(got, msg) {
				t.Errorf("%v: can-isotp received %q", args, line)
			}
		}
	})
}
