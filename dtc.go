package obd2

import (
	"fmt"
	"strconv"
)

// DTC is a diagnostic trouble code such as P0301, held in the two-byte form
// that ECUs send: the top two bits select the system (P, C, B or U) and the
// remaining 14 bits hold the four characters after it.
type DTC uint16

// String formats the code, e.g. "P0301".
func (d DTC) String() string {
	v := uint16(d) // a DTC operand would make %X call String again
	return fmt.Sprintf("%c%d%X%X%X", d.System(), v>>12&0x3, v>>8&0xF, v>>4&0xF, v&0xF)
}

// System returns the letter of the vehicle system the code belongs to:
// 'P' powertrain, 'C' chassis, 'B' body or 'U' network.
func (d DTC) System() byte {
	return "PCBU"[d>>14]
}

// MarshalText encodes the code as text, such as "P0301", so that JSON
// shows it that way.
func (d DTC) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText parses a code such as "P0301".
func (d *DTC) UnmarshalText(b []byte) error {
	v, err := ParseDTC(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// ParseDTC parses a five-character code such as "P0301" or "U0100".
func ParseDTC(s string) (DTC, error) {
	if len(s) != 5 {
		return 0, fmt.Errorf("obd2: invalid DTC %q", s)
	}
	var sys uint16
	switch s[0] {
	case 'P', 'p':
		sys = 0
	case 'C', 'c':
		sys = 1
	case 'B', 'b':
		sys = 2
	case 'U', 'u':
		sys = 3
	default:
		return 0, fmt.Errorf("obd2: invalid DTC %q: unknown system %q", s, s[0])
	}
	if s[1] < '0' || s[1] > '3' {
		return 0, fmt.Errorf("obd2: invalid DTC %q: second character must be 0-3", s)
	}
	rest, err := strconv.ParseUint(s[2:], 16, 16)
	if err != nil {
		return 0, fmt.Errorf("obd2: invalid DTC %q: %w", s, err)
	}
	return DTC(sys<<14 | uint16(s[1]-'0')<<12 | uint16(rest)), nil
}

// ParseDTCs decodes a positive response to service 03, 07 or 0A.
//
// On CAN the response is the service ID, a count byte and two bytes per
// code. On the older protocols there is no count byte: each message holds up
// to three codes, and unused slots are 0000. Codes of 0000 are dropped in
// both cases.
func ParseDTCs(r Response) ([]DTC, error) {
	if len(r.Data) < 1 {
		return nil, fmt.Errorf("obd2: empty DTC response")
	}
	body := r.Data[1:]
	n := len(body) / 2
	if r.Protocol.IsCAN() || r.Protocol == ProtocolUnknown {
		if len(body) < 1 {
			return nil, fmt.Errorf("obd2: DTC response without a count: % X", r.Data)
		}
		n = int(body[0])
		body = body[1:]
		if len(body) < 2*n {
			return nil, fmt.Errorf("obd2: DTC response announces %d codes but carries %d bytes", n, len(body))
		}
	}
	codes := make([]DTC, 0, n)
	for i := range n {
		if d := DTC(uint16(body[2*i])<<8 | uint16(body[2*i+1])); d != 0 {
			codes = append(codes, d)
		}
	}
	return codes, nil
}
