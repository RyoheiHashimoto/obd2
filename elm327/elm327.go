// Package elm327 talks to vehicles through ELM327 adapters and their many
// clones, over any byte stream: a USB or Bluetooth serial port, or a TCP
// connection to a Wi-Fi adapter.
//
//	conn, _ := net.Dial("tcp", "192.168.0.10:35000") // a Wi-Fi adapter
//	adapter, err := elm327.Open(ctx, conn, elm327.Options{})
//	client := obd2.NewClient(adapter)
//
// Through an ELM327 the obd2 package reaches every OBD-II protocol, not only
// CAN: SAE J1850 PWM and VPW, ISO 9141-2 and ISO 14230-4 (KWP2000) as well.
package elm327

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/isotp"
)

// Protocol is a protocol number as the AT SP command takes it.
type Protocol byte

// Protocol numbers. ProtocolAuto makes the adapter search for the vehicle's
// protocol on the first request, which can take several seconds.
const (
	ProtocolAuto      Protocol = '0'
	ProtocolJ1850PWM  Protocol = '1'
	ProtocolJ1850VPW  Protocol = '2'
	ProtocolISO9141   Protocol = '3'
	ProtocolKWPSlow   Protocol = '4' // ISO 14230-4, 5 baud initialization
	ProtocolKWPFast   Protocol = '5' // ISO 14230-4, fast initialization
	ProtocolCAN11_500 Protocol = '6'
	ProtocolCAN29_500 Protocol = '7'
	ProtocolCAN11_250 Protocol = '8'
	ProtocolCAN29_250 Protocol = '9'
)

// Options configures an adapter. The zero value searches for the protocol.
type Options struct {
	// Protocol selects the vehicle protocol. Zero means ProtocolAuto.
	Protocol Protocol
}

// Adapter is an ELM327 adapter. It implements obd2.Transport. It is safe
// for concurrent use; commands are sent one at a time.
type Adapter struct {
	rw io.ReadWriter
	in chan chunk

	mu      sync.Mutex
	buf     []byte
	dirty   bool // a command was abandoned before its prompt arrived
	proto   obd2.Protocol
	version string
}

var _ obd2.Transport = (*Adapter)(nil)

type chunk struct {
	b   []byte
	err error
}

// ErrAdapter is wrapped by the errors the adapter reports, such as CAN ERROR
// or UNABLE TO CONNECT.
var ErrAdapter = errors.New("elm327")

// resyncTimeout bounds the wait for the prompt of an abandoned command. The
// adapter ends every command with a prompt, at the latest when its own
// timeout expires.
const resyncTimeout = 3 * time.Second

// Open initializes the adapter on rw and returns it. Reads from rw happen in
// a background goroutine that ends when rw returns an error, for example
// when it is closed.
func Open(ctx context.Context, rw io.ReadWriter, opt Options) (*Adapter, error) {
	a := &Adapter{rw: rw, in: make(chan chunk, 16)}
	go a.readLoop()

	p := opt.Protocol
	if p == 0 {
		p = ProtocolAuto
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// If the adapter was busy, the first reset only interrupts it.
	for range 2 {
		lines, err := a.command(ctx, "ATZ")
		if err != nil {
			return nil, fmt.Errorf("elm327: reset: %w", err)
		}
		if i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "ELM") }); i >= 0 {
			a.version = lines[i]
			break
		}
	}
	if a.version == "" {
		return nil, fmt.Errorf("%w: no answer to reset; is this an ELM327?", ErrAdapter)
	}
	for _, cmd := range []string{
		"ATE0", // echo off
		"ATL0", // no line feeds
		"ATS0", // no spaces
		"ATH1", // headers on, to tell the ECUs apart
		"ATSP" + string(rune(p)),
	} {
		lines, err := a.command(ctx, cmd)
		if err != nil {
			return nil, fmt.Errorf("elm327: %s: %w", cmd, err)
		}
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, "OK") }) {
			return nil, fmt.Errorf("%w: %s answered %q", ErrAdapter, cmd, lines)
		}
	}
	return a, nil
}

// Version returns the identification the adapter printed on reset, such as
// "ELM327 v1.5". Clones often claim a version they do not implement.
func (a *Adapter) Version() string { return a.version }

// Close closes the underlying stream if it is an io.Closer.
func (a *Adapter) Close() error {
	if c, ok := a.rw.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (a *Adapter) readLoop() {
	for {
		b := make([]byte, 256)
		n, err := a.rw.Read(b)
		if n > 0 {
			a.in <- chunk{b: b[:n]}
		}
		if err != nil {
			a.in <- chunk{err: err}
			close(a.in)
			return
		}
	}
}

// Command sends one command, such as "ATDP" or "0100", and returns the
// lines of the answer. Use it for adapter features this package does not
// cover, like "ATSH7E0" to address one ECU.
func (a *Adapter) Command(ctx context.Context, cmd string) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(cmd)), "ATSP") {
		a.proto = obd2.ProtocolUnknown
	}
	return a.command(ctx, cmd)
}

// command sends cmd and reads the answer up to the prompt. a.mu must be
// held.
func (a *Adapter) command(ctx context.Context, cmd string) ([]string, error) {
	if a.dirty {
		a.resync(ctx)
	}
	a.drain()
	if _, err := io.WriteString(a.rw, cmd+"\r"); err != nil {
		return nil, fmt.Errorf("elm327: write: %w", err)
	}
	out, err := a.readPrompt(ctx)
	if err != nil {
		a.dirty = true
		return nil, err
	}
	var lines []string
	for _, l := range strings.FieldsFunc(string(out), func(r rune) bool { return r == '\r' || r == '\n' }) {
		if l = strings.TrimSpace(l); l != "" && l != cmd {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// resync waits for the prompt of an abandoned command and discards its
// answer. It sends nothing: an empty line would make the adapter repeat the
// last command, which might have been one that clears codes.
func (a *Adapter) resync(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, resyncTimeout)
	defer cancel()
	_, _ = a.readPrompt(rctx) // a timeout is fine: the adapter may have been idle
	a.dirty = false
}

// drain discards output that arrived outside of a command, such as alerts.
func (a *Adapter) drain() {
	a.buf = nil
	for {
		select {
		case c, ok := <-a.in:
			if !ok || c.err != nil {
				return
			}
		default:
			return
		}
	}
}

func (a *Adapter) readPrompt(ctx context.Context) ([]byte, error) {
	for {
		if i := bytes.IndexByte(a.buf, '>'); i >= 0 {
			out := a.buf[:i]
			a.buf = a.buf[i+1:]
			return out, nil
		}
		select {
		case c, ok := <-a.in:
			if !ok {
				return nil, fmt.Errorf("elm327: %w", io.ErrClosedPipe)
			}
			if c.err != nil {
				return nil, fmt.Errorf("elm327: read: %w", c.err)
			}
			a.buf = append(a.buf, c.b...)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// adapterErrors are the messages an ELM327 prints instead of data.
var adapterErrors = []string{
	"?", "ACT ALERT", "BUFFER FULL", "BUS BUSY", "BUS ERROR", "CAN ERROR",
	"DATA ERROR", "FB ERROR", "LP ALERT", "LV RESET", "STOPPED", "UNABLE TO CONNECT",
}

// RoundTrip implements obd2.Transport.
func (a *Adapter) RoundTrip(ctx context.Context, req []byte) ([]obd2.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lines, err := a.command(ctx, strings.ToUpper(hex.EncodeToString(req)))
	if err != nil {
		return nil, err
	}
	var data []string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "SEARCHING"), strings.HasPrefix(l, "BUS INIT"):
			if strings.HasSuffix(l, "ERROR") {
				return nil, fmt.Errorf("%w: %s", ErrAdapter, l)
			}
		case l == "NO DATA":
			return nil, obd2.ErrNoResponse
		case strings.HasPrefix(l, "ERR"), slices.Contains(adapterErrors, strings.TrimLeft(l, "!")):
			return nil, fmt.Errorf("%w: %s", ErrAdapter, l)
		case strings.HasSuffix(l, "<DATA ERROR"), strings.HasSuffix(l, "<RX ERROR"):
			// A damaged line, which the adapter shows anyway. Skip it.
		default:
			data = append(data, l)
		}
	}
	if len(data) == 0 {
		return nil, obd2.ErrNoResponse
	}
	if a.proto == obd2.ProtocolUnknown {
		if err := a.detectProtocol(ctx); err != nil {
			return nil, err
		}
	}
	return parse(data, a.proto)
}

// detectProtocol asks the adapter which protocol it is using.
func (a *Adapter) detectProtocol(ctx context.Context) error {
	lines, err := a.command(ctx, "ATDPN")
	if err != nil {
		return fmt.Errorf("elm327: ATDPN: %w", err)
	}
	if len(lines) == 0 {
		return fmt.Errorf("%w: empty answer to ATDPN", ErrAdapter)
	}
	last := lines[len(lines)-1]
	p, ok := protocols[strings.TrimPrefix(last, "A")]
	if !ok {
		return fmt.Errorf("%w: unknown protocol %q", ErrAdapter, last)
	}
	a.proto = p
	return nil
}

var protocols = map[string]obd2.Protocol{
	"1": obd2.ProtocolJ1850PWM,
	"2": obd2.ProtocolJ1850VPW,
	"3": obd2.ProtocolISO9141,
	"4": obd2.ProtocolKWP2000,
	"5": obd2.ProtocolKWP2000,
	"6": obd2.ProtocolCAN11,
	"7": obd2.ProtocolCAN29,
	"8": obd2.ProtocolCAN11,
	"9": obd2.ProtocolCAN29,
	"A": obd2.ProtocolCAN29, // SAE J1939
	"B": obd2.ProtocolCAN11, // user-defined CAN, 11-bit by default
	"C": obd2.ProtocolCAN11,
}

// parse turns the lines of an answer, printed with headers on and spaces
// off, into responses.
func parse(lines []string, proto obd2.Protocol) ([]obd2.Response, error) {
	if proto.IsCAN() {
		return parseCAN(lines, proto)
	}
	var out []obd2.Response
	for _, l := range lines {
		b, err := hex.DecodeString(l)
		if err != nil {
			return nil, fmt.Errorf("%w: unexpected line %q", ErrAdapter, l)
		}
		// Three header bytes (priority, target, source), the data, and a
		// checksum. KWP2000 adds a length byte to the header when the
		// length bits of the first byte are zero.
		hdr := 3
		if proto == obd2.ProtocolKWP2000 && len(b) > 0 && b[0]&0x3F == 0 {
			hdr = 4
		}
		if len(b) < hdr+2 {
			return nil, fmt.Errorf("%w: line too short: %q", ErrAdapter, l)
		}
		out = append(out, obd2.Response{ECU: uint32(b[2]), Protocol: proto, Data: b[hdr : len(b)-1]})
	}
	return out, nil
}

// parseCAN reassembles CAN frames, which the adapter prints one per line
// with the identifier and the ISO-TP PCI byte in front of the data. Frames
// of different ECUs may interleave.
func parseCAN(lines []string, proto obd2.Protocol) ([]obd2.Response, error) {
	idLen := 3
	if proto == obd2.ProtocolCAN29 {
		idLen = 8
	}
	receivers := map[uint32]*isotp.Receiver{}
	var out []obd2.Response
	for _, l := range lines {
		if len(l) < idLen+2 {
			return nil, fmt.Errorf("%w: line too short: %q", ErrAdapter, l)
		}
		id, err := strconv.ParseUint(l[:idLen], 16, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: unexpected line %q", ErrAdapter, l)
		}
		payload, err := hex.DecodeString(l[idLen:])
		if err != nil {
			return nil, fmt.Errorf("%w: unexpected line %q", ErrAdapter, l)
		}
		r := receivers[uint32(id)]
		if r == nil {
			r = isotp.NewReceiver(isotp.Options{})
			receivers[uint32(id)] = r
		}
		// The adapter sends the flow control frames itself.
		_, msg, err := r.Feed(payload)
		if err != nil {
			return nil, fmt.Errorf("elm327: ECU %X: %w", id, err)
		}
		if msg == nil || len(msg) >= 3 && msg[0] == 0x7F && msg[2] == 0x78 {
			continue // incomplete, or "response pending" before the answer
		}
		out = append(out, obd2.Response{ECU: uint32(id), Protocol: proto, Data: msg})
	}
	if len(out) == 0 {
		return nil, obd2.ErrNoResponse
	}
	return out, nil
}
