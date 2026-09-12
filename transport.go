package obd2

import (
	"context"
	"errors"
	"fmt"
)

// Transport carries OBD requests to a vehicle and returns the ECUs' answers.
//
// This package provides a transport for a raw CAN bus (NewCANTransport), and
// the elm327 package provides one for ELM327 adapters. Other adapters can be
// supported by implementing this interface.
type Transport interface {
	// RoundTrip sends req, a service ID followed by its parameters, and
	// returns the response of every ECU that answered. It returns
	// ErrNoResponse when no ECU answered.
	RoundTrip(ctx context.Context, req []byte) ([]Response, error)
}

// PIDCombiner is implemented by transports that can tell whether a service
// 01 request may ask for several PIDs at once. On CAN, a Client combines up
// to six PIDs per request unless CombinesPIDs returns false. The elm327
// adapter returns false, because many ELM327 clones answer only the first
// PID of a combined request.
type PIDCombiner interface {
	CombinesPIDs() bool
}

// Response is one ECU's answer to a request.
type Response struct {
	// ECU identifies the responder. On CAN it is the identifier the ECU
	// sent from, such as 0x7E8 or 0x18DAF110. On the older protocols it is
	// the source address from the message header, such as 0x10.
	ECU uint32
	// Protocol is the protocol the response arrived on.
	Protocol Protocol
	// Data is the response message, starting with the response service ID
	// (the request's service ID + 0x40), or 0x7F for a negative response.
	//
	// On CAN, a long response arrives as one message. On the older
	// protocols, whose messages hold at most 7 data bytes, it arrives as
	// several Responses from the same ECU.
	Data []byte
}

// Err returns a *NegativeResponseError if r is a negative response, and nil
// otherwise.
func (r Response) Err() error {
	if len(r.Data) >= 3 && r.Data[0] == 0x7F {
		return &NegativeResponseError{ECU: r.ECU, Service: r.Data[1], Code: r.Data[2]}
	}
	return nil
}

// Protocol is an OBD-II communication protocol.
type Protocol int

// The OBD-II protocols. Vehicles sold since about 2008 use CAN.
const (
	ProtocolUnknown  Protocol = iota
	ProtocolCAN11             // ISO 15765-4 CAN, 11-bit identifiers
	ProtocolCAN29             // ISO 15765-4 CAN, 29-bit identifiers
	ProtocolJ1850PWM          // SAE J1850 PWM, 41.6 kbaud
	ProtocolJ1850VPW          // SAE J1850 VPW, 10.4 kbaud
	ProtocolISO9141           // ISO 9141-2
	ProtocolKWP2000           // ISO 14230-4 (KWP2000)
)

// IsCAN reports whether p is one of the ISO 15765-4 CAN protocols.
func (p Protocol) IsCAN() bool {
	return p == ProtocolCAN11 || p == ProtocolCAN29
}

func (p Protocol) String() string {
	switch p {
	case ProtocolCAN11:
		return "ISO 15765-4 CAN (11-bit)"
	case ProtocolCAN29:
		return "ISO 15765-4 CAN (29-bit)"
	case ProtocolJ1850PWM:
		return "SAE J1850 PWM"
	case ProtocolJ1850VPW:
		return "SAE J1850 VPW"
	case ProtocolISO9141:
		return "ISO 9141-2"
	case ProtocolKWP2000:
		return "ISO 14230-4 KWP2000"
	default:
		return "unknown"
	}
}

// ErrNoResponse means no ECU answered a request. For service 01 this is
// also how ECUs say they do not support a PID.
var ErrNoResponse = errors.New("obd2: no response")

// NegativeResponseError is an ECU's refusal of a request: a response with
// service ID 0x7F. Code is the negative response code defined by ISO 14229-1.
type NegativeResponseError struct {
	ECU     uint32
	Service byte // the service ID that was refused
	Code    byte
}

func (e *NegativeResponseError) Error() string {
	name, ok := nrcNames[e.Code]
	if !ok {
		name = "unknown reason"
	}
	return fmt.Sprintf("obd2: ECU %X refused service %02X: %s (NRC %02X)", e.ECU, e.Service, name, e.Code)
}

// nrcResponsePending is the negative response code an ECU sends to say it
// needs more time; the real answer follows.
const nrcResponsePending = 0x78

var nrcNames = map[byte]string{
	0x10: "general reject",
	0x11: "service not supported",
	0x12: "sub-function not supported",
	0x13: "incorrect message length or invalid format",
	0x14: "response too long",
	0x21: "busy, repeat request",
	0x22: "conditions not correct",
	0x24: "request sequence error",
	0x31: "request out of range",
	0x33: "security access denied",
	0x35: "invalid key",
	0x36: "exceeded number of attempts",
	0x37: "required time delay not expired",
	0x72: "general programming failure",
	0x78: "response pending",
	0x7E: "sub-function not supported in active session",
	0x7F: "service not supported in active session",
}
