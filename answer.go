package obd2

import "bytes"

// answers reports whether resp can be the answer to req.
//
// A bus can carry another tester, such as a dashboard that polls the ECU
// itself. ECUs answer its requests too, and nothing in an 11-bit CAN answer
// says which tester it is for. The service ID and the parameters an answer
// echoes set apart the answers to other requests. Answers to the same
// request cannot be told apart, and need not be.
func answers(req, resp []byte) bool {
	if len(req) == 0 || len(resp) == 0 {
		return false
	}
	if resp[0] == 0x7F {
		return len(resp) >= 2 && resp[1] == req[0]
	}
	if resp[0] != req[0]+0x40 {
		return false
	}
	switch req[0] {
	case 0x01, 0x02:
		// The answer starts with one of the requested PIDs.
		return len(req) >= 2 && len(resp) >= 2 && bytes.IndexByte(req[1:], resp[1]) >= 0
	case 0x06, 0x09:
		return len(req) >= 2 && len(resp) >= 2 && resp[1] == req[1]
	case 0x22:
		return len(req) >= 3 && len(resp) >= 3 && resp[1] == req[1] && resp[2] == req[2]
	}
	return true
}
