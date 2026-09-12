package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/RyoheiHashimoto/obd2"
)

// output writes the results of commands as text, or as JSON.
//
// In JSON, identifiers are hex strings as OBD documents write them: an ECU
// is "7E8" or "18DAF110", a PID is "0C", and raw bytes are "1AF8".
type output struct {
	w    io.Writer
	json bool
}

type pidJSON struct {
	PID  string `json:"pid"`
	Name string `json:"name"`
}

type readingJSON struct {
	ECU   string   `json:"ecu"`
	PID   string   `json:"pid"`
	Name  string   `json:"name"`
	Value *float64 `json:"value"` // null when the PID is not a single number
	Unit  string   `json:"unit,omitempty"`
	Raw   string   `json:"raw"`
}

type responseJSON struct {
	ECU  string `json:"ecu"`
	Data string `json:"data"`
}

// dtcReport is the result of the dtc command. A nil list, null in JSON,
// means that no ECU supports that service.
type dtcReport struct {
	MIL       bool       `json:"mil"`
	Confirmed int        `json:"confirmed"`
	Stored    []obd2.DTC `json:"stored"`
	Pending   []obd2.DTC `json:"pending"`
	Permanent []obd2.DTC `json:"permanent"`
}

func (o *output) encode(v any) error {
	return json.NewEncoder(o.w).Encode(v)
}

func (o *output) printf(format string, a ...any) error {
	_, err := fmt.Fprintf(o.w, format, a...)
	return err
}

func (o *output) info(p obd2.Protocol, pids []obd2.PID) error {
	if o.json {
		list := []pidJSON{}
		for _, id := range pids {
			list = append(list, pidJSON{PID: pidHex(id), Name: id.String()})
		}
		return o.encode(struct {
			Protocol string    `json:"protocol"`
			PIDs     []pidJSON `json:"pids"`
		}{p.String(), list})
	}
	if err := o.printf("Protocol: %v\n", p); err != nil {
		return err
	}
	for _, id := range pids {
		if err := o.printf("  %s  %v\n", pidHex(id), id); err != nil {
			return err
		}
	}
	return nil
}

func (o *output) readings(rs obd2.Readings, now time.Time) error {
	if o.json {
		list := []readingJSON{}
		for _, r := range rs {
			info, _ := r.PID.Info()
			rj := readingJSON{
				ECU:  ecuHex(r.ECU),
				PID:  pidHex(r.PID),
				Name: r.PID.String(),
				Unit: info.Unit,
				Raw:  strings.ToUpper(hex.EncodeToString(r.Data)),
			}
			if v, err := r.Float(); err == nil {
				rj.Value = &v
			}
			list = append(list, rj)
		}
		return o.encode(struct {
			Time     string        `json:"time"`
			Readings []readingJSON `json:"readings"`
		}{now.UTC().Format(time.RFC3339Nano), list})
	}
	for _, r := range rs {
		if err := o.printf("%-8s %v\n", ecuHex(r.ECU), r); err != nil {
			return err
		}
	}
	return nil
}

// separator ends one round of watch. JSON needs none: each round is a line.
func (o *output) separator() error {
	if o.json {
		return nil
	}
	return o.printf("\n")
}

func (o *output) dtcs(r dtcReport) error {
	if o.json {
		return o.encode(r)
	}
	light := "off"
	if r.MIL {
		light = "on"
	}
	if err := o.printf("Check engine light: %s, confirmed codes: %d\n", light, r.Confirmed); err != nil {
		return err
	}
	for _, k := range []struct {
		name  string
		codes []obd2.DTC
	}{{"Stored:", r.Stored}, {"Pending:", r.Pending}, {"Permanent:", r.Permanent}} {
		if k.codes == nil {
			if err := o.printf("%-10s not supported\n", k.name); err != nil {
				return err
			}
			continue
		}
		if err := o.printf("%-10s %v\n", k.name, k.codes); err != nil {
			return err
		}
	}
	return nil
}

func (o *output) cleared() error {
	if o.json {
		return o.encode(map[string]bool{"cleared": true})
	}
	return o.printf("Trouble codes cleared.\n")
}

func (o *output) vin(vin string) error {
	if o.json {
		return o.encode(map[string]string{"vin": vin})
	}
	return o.printf("%s\n", vin)
}

func (o *output) responses(rs []obd2.Response) error {
	if o.json {
		list := []responseJSON{}
		for _, r := range rs {
			list = append(list, responseJSON{ECU: ecuHex(r.ECU), Data: strings.ToUpper(hex.EncodeToString(r.Data))})
		}
		return o.encode(map[string][]responseJSON{"responses": list})
	}
	for _, r := range rs {
		if err := o.printf("%-8s % X\n", ecuHex(r.ECU), r.Data); err != nil {
			return err
		}
	}
	return nil
}

func ecuHex(id uint32) string  { return fmt.Sprintf("%X", id) }
func pidHex(p obd2.PID) string { return fmt.Sprintf("%02X", byte(p)) }
