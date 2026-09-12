# obd2

OBD-II diagnostics for Go. Read live data, trouble codes and the VIN from a
vehicle, either straight from a CAN bus (Linux SocketCAN) or through an ELM327
adapter.

- **Every OBD-II protocol.** CAN (ISO 15765-4, 11-bit and 29-bit identifiers,
  detected automatically) directly; SAE J1850, ISO 9141-2 and KWP2000 through
  an ELM327.
- **Any ELM327.** USB, Bluetooth or Wi-Fi: the adapter runs over any
  `io.ReadWriter`.
- **ISO-TP in pure Go.** The `isotp` package implements ISO 15765-2 in user
  space, so it needs no kernel module, and it is usable on its own for UDS or
  any other protocol that runs on ISO-TP.
- **Testable without a car.** `ecusim` simulates ECUs on a virtual bus or on a
  vcan interface.
- No cgo. One dependency, `golang.org/x/sys`, for SocketCAN.

## Install

```sh
go get github.com/RyoheiHashimoto/obd2
```

## Usage

### A CAN interface (Linux)

```go
bus, err := socketcan.Open("can0") // ip link set can0 up type can bitrate 500000
if err != nil {
	log.Fatal(err)
}
defer bus.Close()

client := obd2.NewClient(obd2.NewCANTransport(bus, obd2.CANOptions{}))

readings, err := client.Query(ctx, obd2.EngineRPM, obd2.VehicleSpeed, obd2.CoolantTemp)
if err != nil {
	log.Fatal(err)
}
for _, r := range readings {
	fmt.Println(r) // Engine speed: 1726 rpm
}
rpm, err := readings.Float(obd2.EngineRPM)
```

Each SocketCAN socket receives its own copy of the bus traffic, so a program
that also monitors the bus can open a second socket for OBD.

### An ELM327 adapter

```go
conn, err := net.Dial("tcp", "192.168.0.10:35000") // a Wi-Fi adapter
if err != nil {
	log.Fatal(err)
}
adapter, err := elm327.Open(ctx, conn, elm327.Options{})
if err != nil {
	log.Fatal(err)
}
defer adapter.Close()

client := obd2.NewClient(adapter)
vin, err := client.VIN(ctx)
```

For a USB or Bluetooth adapter, pass a serial port from any serial package,
for example [go.bug.st/serial](https://pkg.go.dev/go.bug.st/serial):

```go
port, err := serial.Open("/dev/ttyUSB0", &serial.Mode{BaudRate: 38400})
adapter, err := elm327.Open(ctx, port, elm327.Options{})
```

By default the adapter tries CAN (11-bit, 500 kbit/s) first and searches the
other protocols if that fails. The search runs on the first request and can
take several seconds, so give that request a generous context deadline. The
protocol is not saved in the adapter.

### Trouble codes

```go
on, count, err := client.MILStatus(ctx) // check engine light and number of codes
codes, err := client.StoredDTCs(ctx)    // []obd2.DTC; fmt.Println prints "P0133"
pending, err := client.PendingDTCs(ctx)
err = client.ClearDTCs(ctx) // also resets the readiness monitors
```

### Manufacturer data

Service 22 reads data identifiers that manufacturers define beyond the
standard PIDs. Some ECUs only answer it when addressed directly:

```go
engine := obd2.NewClient(obd2.NewCANTransport(bus, obd2.CANOptions{ECU: 0x7E0}))
data, err := engine.ReadDataByIdentifier(ctx, 0x17B3)
```

`Client.Request` sends any raw request and returns every ECU's answer.

### Testing without a vehicle

```go
bus := can.NewVirtualBus()
sim := &ecusim.ECU{
	Address: 0x7E0,
	PIDs:    map[obd2.PID][]byte{obd2.EngineRPM: {0x1A, 0xF8}},
	Stored:  []obd2.DTC{0x0133},
	VIN:     "1M8GDM9AXKP042788",
}
go sim.Serve(ctx, bus.Connect())

client := obd2.NewClient(obd2.NewCANTransport(bus.Connect(), obd2.CANOptions{}))
```

The simulator also runs on a Linux vcan interface, so programs that expect a
real CAN device can be tested too.

## Coverage

| Service | Client method |
|---|---|
| 01 current data | `Query`, `SupportedPIDs`, `MILStatus` |
| 03 / 07 / 0A trouble codes | `StoredDTCs`, `PendingDTCs`, `PermanentDTCs` |
| 04 clear codes | `ClearDTCs` |
| 09 vehicle information | `VIN` |
| 22 read data by identifier | `ReadDataByIdentifier` |
| anything else | `Request` |

About a hundred standard PIDs are defined with their names, units and
formulas (SAE J1979). PIDs whose value is a bit field or several numbers are
returned as raw bytes.

| Protocol | `socketcan` | `elm327` |
|---|---|---|
| ISO 15765-4 CAN, 11-bit | yes | yes |
| ISO 15765-4 CAN, 29-bit | yes | yes |
| SAE J1850 PWM / VPW | | yes |
| ISO 9141-2 | | yes |
| ISO 14230-4 KWP2000 | | yes |

On CAN, a request to all ECUs waits for `CANOptions.Timeout` (100 ms by
default) to collect every answer. For fast polling, address one ECU with
`CANOptions.ECU`: the request then returns as soon as that ECU answers.

## How it is tested

- The ISO-TP layer, the CAN transport and the client are tested end to end
  against simulated ECUs, including several ECUs answering at once,
  multi-frame messages, 29-bit addressing and "response pending" replies.
- The ELM327 parser is tested with the exchanges printed in the ELM327 data
  sheet, including interleaved answers from two ECUs and the formats of the
  older protocols.
- In CI, the SocketCAN code runs against the Linux kernel's CAN stack (vcan),
  and the ISO-TP implementation is checked in both directions against
  [can-isotp](https://github.com/pylessard/python-can-isotp), an independent
  implementation in Python.

### Tested vehicles

| Vehicle | Protocol | Adapter | Works |
|---|---|---|---|
| Mazda Demio DY (DBA-DY3W, ZJ-VE 1.3 L), Japanese market | CAN 11-bit, 500 kbit/s | SocketCAN (MCP2515) | 28 PIDs, trouble codes, VIN, service 22 |

On the Demio, the VIN request returns the vehicle's 10-character Japanese
chassis number rather than a 17-character VIN. Its bus also carries a
dashboard ([pi-obd-meter](https://github.com/RyoheiHashimoto/pi-obd-meter))
that polls the ECU itself; the client recognizes the answers to the
dashboard's requests and ignores them.

Reports from other vehicles are welcome:
[open a vehicle report](https://github.com/RyoheiHashimoto/obd2/issues/new?template=vehicle-report.yml).

## Not yet supported

Freeze frames (service 02), on-board monitoring results (service 06), CAN FD,
SLCAN serial CAN adapters, and SAE J1939.

## License

MIT
