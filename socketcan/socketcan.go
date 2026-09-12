// Package socketcan connects to CAN interfaces through Linux SocketCAN.
//
// Any adapter with a SocketCAN driver works: SPI boards such as MCP2515
// HATs, USB adapters (gs_usb, PCAN-USB, Kvaser and others), and virtual vcan
// interfaces. Bring the interface up first, for OBD usually at 500 kbit/s:
//
//	ip link set can0 up type can bitrate 500000
//
// On other operating systems Open returns an error.
package socketcan

// Filter selects received frames by identifier: a frame passes when
// frame.ID & Mask == ID & Mask and its identifier length matches Extended.
type Filter struct {
	ID       uint32
	Mask     uint32
	Extended bool
}
