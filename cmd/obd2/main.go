// Command obd2 reads diagnostic data from a vehicle through a SocketCAN
// interface or an ELM327 adapter.
//
//	obd2 -can can0 info
//	obd2 -elm 192.168.0.10:35000 dtc
//	obd2 -elm /dev/ttyUSB0 watch 0C 0D
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

func main() {
	canIf := flag.String("can", "", "SocketCAN interface, such as can0 (Linux)")
	elmAddr := flag.String("elm", "", "ELM327 adapter: host:port for Wi-Fi, or a serial device path")
	ecu := flag.String("ecu", "", "address one ECU instead of all: its CAN request ID in hex, such as 7E0")
	timeout := flag.Duration("timeout", 10*time.Second, "time limit for each request")
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
		err = run(ctx, *canIf, *elmAddr, *ecu, *timeout, flag.Arg(0), flag.Args()[1:])
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "obd2:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, canIf, elmAddr, ecu string, timeout time.Duration, cmd string, args []string) error {
	var ecuID uint64
	if ecu != "" {
		var err error
		if ecuID, err = strconv.ParseUint(ecu, 16, 32); err != nil {
			return fmt.Errorf("bad -ecu %q: %w", ecu, err)
		}
	}
	var t obd2.Transport
	switch {
	case canIf != "" && elmAddr != "":
		return errors.New("use -can or -elm, not both")
	case canIf != "":
		bus, err := socketcan.Open(canIf)
		if err != nil {
			return err
		}
		defer func() { _ = bus.Close() }()
		t = obd2.NewCANTransport(bus, obd2.CANOptions{ECU: uint32(ecuID)})
	case elmAddr != "":
		rw, err := dial(elmAddr)
		if err != nil {
			return err
		}
		octx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		a, err := elm327.Open(octx, rw, elm327.Options{})
		if err != nil {
			_ = rw.Close()
			return err
		}
		defer func() { _ = a.Close() }()
		if ecu != "" {
			if _, err := a.Command(octx, "ATSH"+strings.ToUpper(ecu)); err != nil {
				return err
			}
		}
		t = a
	default:
		return errors.New("give the vehicle connection with -can or -elm")
	}
	c := obd2.NewClient(t)

	// with runs f with a time limit of its own.
	with := func(f func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
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
			fmt.Println("Protocol:", c.Protocol())
			for _, p := range pids {
				fmt.Printf("  %02X  %v\n", byte(p), p)
			}
			return nil
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
			if err := with(func(ctx context.Context) error {
				rs, err := c.Query(ctx, pids...)
				for _, r := range rs {
					fmt.Printf("%-8X %v\n", r.ECU, r)
				}
				return err
			}); err != nil || cmd == "read" {
				return err
			}
			fmt.Println()
		}
	case "dtc":
		return with(func(ctx context.Context) error {
			on, n, err := c.MILStatus(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("Check engine light: %v, confirmed codes: %d\n", map[bool]string{true: "on", false: "off"}[on], n)
			for _, kind := range []struct {
				name string
				read func(context.Context) ([]obd2.DTC, error)
			}{{"Stored", c.StoredDTCs}, {"Pending", c.PendingDTCs}, {"Permanent", c.PermanentDTCs}} {
				codes, err := kind.read(ctx)
				switch {
				case errors.Is(err, obd2.ErrNoResponse):
					fmt.Printf("%-10s not supported\n", kind.name+":")
				case err != nil:
					return err
				default:
					fmt.Printf("%-10s %v\n", kind.name+":", codes)
				}
			}
			return nil
		})
	case "clear":
		if len(args) != 1 || args[0] != "-yes" {
			return errors.New("clearing also resets the readiness monitors; run 'obd2 clear -yes' to confirm")
		}
		return with(c.ClearDTCs)
	case "vin":
		return with(func(ctx context.Context) error {
			vin, err := c.VIN(ctx)
			if err == nil {
				fmt.Println(vin)
			}
			return err
		})
	case "raw":
		req, err := hex.DecodeString(strings.Join(args, ""))
		if err != nil || len(req) == 0 {
			return fmt.Errorf("raw needs a request in hex, such as 0902")
		}
		return with(func(ctx context.Context) error {
			rs, err := c.Request(ctx, req)
			for _, r := range rs {
				fmt.Printf("%-8X % X\n", r.ECU, r.Data)
			}
			return err
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
