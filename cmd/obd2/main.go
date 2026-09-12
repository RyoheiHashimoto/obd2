// Command obd2 reads diagnostic data from a vehicle through a SocketCAN
// interface or an ELM327 adapter.
//
//	obd2 -can can0 info
//	obd2 -elm 192.168.0.10:35000 dtc
//	obd2 -elm /dev/ttyUSB0 watch 0C 0D
//	obd2 -can can0 -json read 0C 0D
//
// With -json every command prints JSON for other programs to read; watch
// prints one JSON object per line.
//
// A serial device is opened as a file, so set its speed first, for example
// "stty -F /dev/ttyUSB0 38400 raw -echo". On macOS use the /dev/cu.* device.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/RyoheiHashimoto/obd2"
	"github.com/RyoheiHashimoto/obd2/ecusim"
	"github.com/RyoheiHashimoto/obd2/elm327"
	"github.com/RyoheiHashimoto/obd2/socketcan"
)

const usageText = `usage: obd2 [flags] command [arguments]

Commands:
  info          the protocol and the PIDs the vehicle supports
  read [PID..]  read PIDs, given in hex (0C 0D); all numeric ones if none
  watch PID..   read PIDs repeatedly until interrupted
  dtc           the check engine light and the trouble codes
  clear -yes    clear the trouble codes (this resets the readiness monitors)
  vin           the vehicle identification number
  raw HEX       send a raw request, such as 0902, and print every answer
  sim           run a simulated engine ECU on the -can interface (for testing)

Flags:
`

// options holds the settings that apply to every command.
type options struct {
	timeout time.Duration // limit for each request
	json    bool          // print JSON instead of text
}

func main() {
	canIf := flag.String("can", "", "SocketCAN interface, such as can0 (Linux)")
	elmAddr := flag.String("elm", "", "ELM327 adapter: host:port for Wi-Fi, or a serial device path")
	ecu := flag.String("ecu", "", "address one ECU instead of all: its CAN request ID in hex, such as 7E0")
	timeout := flag.Duration("timeout", 10*time.Second, "time limit for each request")
	asJSON := flag.Bool("json", false, "print results as JSON; watch prints one object per line")
	flag.Usage = func() {
		_, _ = fmt.Fprint(flag.CommandLine.Output(), usageText)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	if flag.Arg(0) == "sim" {
		err = simulate(ctx, *canIf)
	} else {
		opt := options{timeout: *timeout, json: *asJSON}
		err = connectAndRun(ctx, *canIf, *elmAddr, *ecu, opt, flag.Arg(0), flag.Args()[1:])
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "obd2:", err)
		os.Exit(1)
	}
}

func connectAndRun(ctx context.Context, canIf, elmAddr, ecu string, opt options, cmd string, args []string) error {
	t, closer, err := connect(ctx, canIf, elmAddr, ecu, opt.timeout)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	return run(ctx, obd2.NewClient(t), os.Stdout, opt, cmd, args)
}

// connect opens the vehicle connection that the flags describe.
func connect(ctx context.Context, canIf, elmAddr, ecu string, timeout time.Duration) (obd2.Transport, io.Closer, error) {
	var ecuID uint64
	if ecu != "" {
		var err error
		if ecuID, err = strconv.ParseUint(ecu, 16, 32); err != nil {
			return nil, nil, fmt.Errorf("bad -ecu %q: %w", ecu, err)
		}
	}
	switch {
	case canIf != "" && elmAddr != "":
		return nil, nil, errors.New("use -can or -elm, not both")
	case canIf != "":
		bus, err := socketcan.Open(canIf)
		if err != nil {
			return nil, nil, err
		}
		return obd2.NewCANTransport(bus, obd2.CANOptions{ECU: uint32(ecuID)}), bus, nil
	case elmAddr != "":
		rw, err := dial(elmAddr)
		if err != nil {
			return nil, nil, err
		}
		octx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		a, err := elm327.Open(octx, rw, elm327.Options{})
		if err != nil {
			_ = rw.Close()
			return nil, nil, err
		}
		if ecu != "" {
			if _, err := a.Command(octx, "ATSH"+strings.ToUpper(ecu)); err != nil {
				_ = a.Close()
				return nil, nil, err
			}
		}
		return a, a, nil
	default:
		return nil, nil, errors.New("give the vehicle connection with -can or -elm")
	}
}

// run executes one command and writes its result to w.
func run(ctx context.Context, c *obd2.Client, w io.Writer, opt options, cmd string, args []string) error {
	out := &output{w: w, json: opt.json}
	// with runs f under a time limit of its own.
	with := func(f func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(ctx, opt.timeout)
		defer cancel()
		return f(ctx)
	}

	switch cmd {
	case "info":
		return with(func(ctx context.Context) error {
			pids, err := c.SupportedPIDs(ctx)
			if err != nil {
				return err
			}
			return out.info(c.Protocol(), pids)
		})
	case "read", "watch":
		pids, err := parsePIDs(args)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			if cmd == "watch" {
				return errors.New("watch needs at least one PID")
			}
			if err := with(func(ctx context.Context) error {
				pids, err = numericPIDs(ctx, c)
				return err
			}); err != nil {
				return err
			}
		}
		for {
			err := with(func(ctx context.Context) error {
				rs, err := c.Query(ctx, pids...)
				if err != nil {
					return err
				}
				return out.readings(rs, time.Now())
			})
			if err != nil || cmd == "read" {
				return err
			}
			if err := out.separator(); err != nil {
				return err
			}
		}
	case "dtc":
		return with(func(ctx context.Context) error {
			on, n, err := c.MILStatus(ctx)
			if err != nil {
				return err
			}
			r := dtcReport{MIL: on, Confirmed: n}
			for _, kind := range []struct {
				dst  *[]obd2.DTC
				read func(context.Context) ([]obd2.DTC, error)
			}{{&r.Stored, c.StoredDTCs}, {&r.Pending, c.PendingDTCs}, {&r.Permanent, c.PermanentDTCs}} {
				codes, err := kind.read(ctx)
				if err != nil && !errors.Is(err, obd2.ErrNoResponse) {
					return err
				}
				*kind.dst = codes // nil when no ECU supports the service
			}
			return out.dtcs(r)
		})
	case "clear":
		if len(args) != 1 || args[0] != "-yes" {
			return errors.New("clearing also resets the readiness monitors; run 'obd2 clear -yes' to confirm")
		}
		return with(func(ctx context.Context) error {
			if err := c.ClearDTCs(ctx); err != nil {
				return err
			}
			return out.cleared()
		})
	case "vin":
		return with(func(ctx context.Context) error {
			vin, err := c.VIN(ctx)
			if err != nil {
				return err
			}
			return out.vin(vin)
		})
	case "raw":
		req, err := hex.DecodeString(strings.Join(args, ""))
		if err != nil || len(req) == 0 {
			return errors.New("raw needs a request in hex, such as 0902")
		}
		return with(func(ctx context.Context) error {
			rs, err := c.Request(ctx, req)
			if err != nil {
				return err
			}
			return out.responses(rs)
		})
	default:
		return fmt.Errorf("unknown command %q; run obd2 -h", cmd)
	}
}

// dial connects to a Wi-Fi adapter at host:port, or opens a serial device.
func dial(addr string) (io.ReadWriteCloser, error) {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return net.DialTimeout("tcp", addr, 5*time.Second)
	}
	return os.OpenFile(addr, os.O_RDWR, 0)
}

func parsePIDs(args []string) ([]obd2.PID, error) {
	var pids []obd2.PID
	for _, a := range args {
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(a), "0x"), 16, 8)
		if err != nil {
			return nil, fmt.Errorf("bad PID %q: give it in hex, such as 0C", a)
		}
		pids = append(pids, obd2.PID(v))
	}
	return pids, nil
}

// numericPIDs returns the supported PIDs that decode to a number.
func numericPIDs(ctx context.Context, c *obd2.Client) ([]obd2.PID, error) {
	all, err := c.SupportedPIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []obd2.PID
	for _, p := range all {
		if info, ok := p.Info(); ok && info.Numeric() {
			out = append(out, p)
		}
	}
	return out, nil
}

// simulate serves a simulated engine ECU on a CAN interface until
// interrupted, for trying out OBD software on a vcan interface.
func simulate(ctx context.Context, canIf string) error {
	if canIf == "" {
		return errors.New("sim needs -can, such as -can vcan0")
	}
	bus, err := socketcan.Open(canIf)
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()
	engine := &ecusim.ECU{
		Address: 0x7E0,
		PIDs: map[obd2.PID][]byte{
			obd2.MonitorStatus:    {0x81, 0x07, 0x65, 0x00},
			obd2.EngineLoad:       {0x40},
			obd2.CoolantTemp:      {0x7B},
			obd2.EngineRPM:        {0x1A, 0xF8},
			obd2.VehicleSpeed:     {0x32},
			obd2.IntakeAirTemp:    {0x46},
			obd2.ThrottlePosition: {0x30},
			obd2.FuelTankLevel:    {0x9A},
		},
		Stored: []obd2.DTC{0x0133},
		VIN:    "1M8GDM9AXKP042788",
	}
	fmt.Fprintf(os.Stderr, "simulating an engine ECU (7E0/7E8) on %s; press Ctrl-C to stop\n", canIf)
	return engine.Serve(ctx, bus)
}
