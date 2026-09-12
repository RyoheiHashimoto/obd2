package obd2

import "fmt"

// PID identifies a parameter of service 01 (current data) or service 02
// (freeze frame data), as defined by SAE J1979.
type PID byte

// Standard PIDs. Each vehicle supports a different subset; use
// Client.SupportedPIDs to find out which.
const (
	PIDsSupported01To20         PID = 0x00
	MonitorStatus               PID = 0x01
	FreezeFrameDTC              PID = 0x02
	FuelSystemStatus            PID = 0x03
	EngineLoad                  PID = 0x04
	CoolantTemp                 PID = 0x05
	ShortTermFuelTrimBank1      PID = 0x06
	LongTermFuelTrimBank1       PID = 0x07
	ShortTermFuelTrimBank2      PID = 0x08
	LongTermFuelTrimBank2       PID = 0x09
	FuelPressure                PID = 0x0A
	IntakeManifoldPressure      PID = 0x0B
	EngineRPM                   PID = 0x0C
	VehicleSpeed                PID = 0x0D
	TimingAdvance               PID = 0x0E
	IntakeAirTemp               PID = 0x0F
	MAFAirFlowRate              PID = 0x10
	ThrottlePosition            PID = 0x11
	SecondaryAirStatus          PID = 0x12
	O2SensorsPresent            PID = 0x13
	O2Sensor1                   PID = 0x14 // through O2Sensor1+7 (0x1B)
	OBDStandard                 PID = 0x1C
	O2SensorsPresent4Banks      PID = 0x1D
	AuxiliaryInputStatus        PID = 0x1E
	RunTimeSinceStart           PID = 0x1F
	PIDsSupported21To40         PID = 0x20
	DistanceWithMIL             PID = 0x21
	FuelRailPressure            PID = 0x22
	FuelRailGaugePressure       PID = 0x23
	O2Sensor1RatioVoltage       PID = 0x24 // through O2Sensor1RatioVoltage+7 (0x2B)
	CommandedEGR                PID = 0x2C
	EGRError                    PID = 0x2D
	CommandedEvapPurge          PID = 0x2E
	FuelTankLevel               PID = 0x2F
	WarmUpsSinceCodesCleared    PID = 0x30
	DistanceSinceCodesCleared   PID = 0x31
	EvapVaporPressure           PID = 0x32
	BarometricPressure          PID = 0x33
	O2Sensor1RatioCurrent       PID = 0x34 // through O2Sensor1RatioCurrent+7 (0x3B)
	CatalystTempBank1Sensor1    PID = 0x3C
	CatalystTempBank2Sensor1    PID = 0x3D
	CatalystTempBank1Sensor2    PID = 0x3E
	CatalystTempBank2Sensor2    PID = 0x3F
	PIDsSupported41To60         PID = 0x40
	MonitorStatusThisDriveCycle PID = 0x41
	ControlModuleVoltage        PID = 0x42
	AbsoluteLoad                PID = 0x43
	CommandedEquivalenceRatio   PID = 0x44
	RelativeThrottlePosition    PID = 0x45
	AmbientAirTemp              PID = 0x46
	AbsoluteThrottlePositionB   PID = 0x47
	AbsoluteThrottlePositionC   PID = 0x48
	AcceleratorPedalPositionD   PID = 0x49
	AcceleratorPedalPositionE   PID = 0x4A
	AcceleratorPedalPositionF   PID = 0x4B
	CommandedThrottleActuator   PID = 0x4C
	TimeRunWithMIL              PID = 0x4D
	TimeSinceCodesCleared       PID = 0x4E
	MaximumValues               PID = 0x4F
	MaximumMAFAirFlowRate       PID = 0x50
	FuelType                    PID = 0x51
	EthanolFuelPercent          PID = 0x52
	AbsoluteEvapVaporPressure   PID = 0x53
	EvapVaporPressureWide       PID = 0x54
	ShortTermSecondaryO2Trim13  PID = 0x55
	LongTermSecondaryO2Trim13   PID = 0x56
	ShortTermSecondaryO2Trim24  PID = 0x57
	LongTermSecondaryO2Trim24   PID = 0x58
	FuelRailAbsolutePressure    PID = 0x59
	RelativeAcceleratorPosition PID = 0x5A
	HybridBatteryRemainingLife  PID = 0x5B
	EngineOilTemp               PID = 0x5C
	FuelInjectionTiming         PID = 0x5D
	EngineFuelRate              PID = 0x5E
	EmissionRequirements        PID = 0x5F
	PIDsSupported61To80         PID = 0x60
	DriverDemandTorque          PID = 0x61
	ActualEngineTorque          PID = 0x62
	EngineReferenceTorque       PID = 0x63
	PIDsSupported81ToA0         PID = 0x80
	PIDsSupportedA1ToC0         PID = 0xA0
	Odometer                    PID = 0xA6
	PIDsSupportedC1ToE0         PID = 0xC0
)

// Info describes a PID.
type Info struct {
	Name string // e.g. "Engine speed"
	Unit string // unit of the value Reading.Float returns, e.g. "rpm"
	Size int    // number of data bytes in a response

	decode func(d []byte) float64
}

// Numeric reports whether the PID's value is a single number that
// Reading.Float can decode. Bit fields and PIDs that pack several values
// are not numeric; read their bytes from Reading.Data.
func (i Info) Numeric() bool { return i.decode != nil }

// Info returns the description of p, and false if this package does not
// know p.
func (p PID) Info() (Info, bool) {
	i, ok := pidTable[p]
	return i, ok
}

// String returns the PID's name, or its number when the PID is unknown.
func (p PID) String() string {
	if i, ok := pidTable[p]; ok {
		return i.Name
	}
	return fmt.Sprintf("PID %02X", byte(p))
}

func u16(d []byte) float64  { return float64(uint16(d[0])<<8 | uint16(d[1])) }
func pct(d []byte) float64  { return float64(d[0]) * 100 / 255 }
func temp(d []byte) float64 { return float64(d[0]) - 40 }
func trim(d []byte) float64 { return (float64(d[0]) - 128) * 100 / 128 }
func a(d []byte) float64    { return float64(d[0]) }

var pidTable = map[PID]Info{
	MonitorStatus:               {Name: "Monitor status since DTCs cleared", Size: 4},
	FreezeFrameDTC:              {Name: "DTC that caused the freeze frame", Size: 2},
	FuelSystemStatus:            {Name: "Fuel system status", Size: 2},
	EngineLoad:                  {"Calculated engine load", "%", 1, pct},
	CoolantTemp:                 {"Engine coolant temperature", "°C", 1, temp},
	ShortTermFuelTrimBank1:      {"Short term fuel trim, bank 1", "%", 1, trim},
	LongTermFuelTrimBank1:       {"Long term fuel trim, bank 1", "%", 1, trim},
	ShortTermFuelTrimBank2:      {"Short term fuel trim, bank 2", "%", 1, trim},
	LongTermFuelTrimBank2:       {"Long term fuel trim, bank 2", "%", 1, trim},
	FuelPressure:                {"Fuel pressure (gauge)", "kPa", 1, func(d []byte) float64 { return 3 * a(d) }},
	IntakeManifoldPressure:      {"Intake manifold absolute pressure", "kPa", 1, a},
	EngineRPM:                   {"Engine speed", "rpm", 2, func(d []byte) float64 { return u16(d) / 4 }},
	VehicleSpeed:                {"Vehicle speed", "km/h", 1, a},
	TimingAdvance:               {"Timing advance", "° before TDC", 1, func(d []byte) float64 { return a(d)/2 - 64 }},
	IntakeAirTemp:               {"Intake air temperature", "°C", 1, temp},
	MAFAirFlowRate:              {"Mass air flow rate", "g/s", 2, func(d []byte) float64 { return u16(d) / 100 }},
	ThrottlePosition:            {"Throttle position", "%", 1, pct},
	SecondaryAirStatus:          {Name: "Commanded secondary air status", Size: 1},
	O2SensorsPresent:            {Name: "Oxygen sensors present (2 banks)", Size: 1},
	OBDStandard:                 {Name: "OBD standard the vehicle conforms to", Size: 1},
	O2SensorsPresent4Banks:      {Name: "Oxygen sensors present (4 banks)", Size: 1},
	AuxiliaryInputStatus:        {Name: "Auxiliary input status", Size: 1},
	RunTimeSinceStart:           {"Run time since engine start", "s", 2, u16},
	DistanceWithMIL:             {"Distance traveled with MIL on", "km", 2, u16},
	FuelRailPressure:            {"Fuel rail pressure (relative to manifold vacuum)", "kPa", 2, func(d []byte) float64 { return 0.079 * u16(d) }},
	FuelRailGaugePressure:       {"Fuel rail gauge pressure", "kPa", 2, func(d []byte) float64 { return 10 * u16(d) }},
	CommandedEGR:                {"Commanded EGR", "%", 1, pct},
	EGRError:                    {"EGR error", "%", 1, trim},
	CommandedEvapPurge:          {"Commanded evaporative purge", "%", 1, pct},
	FuelTankLevel:               {"Fuel tank level input", "%", 1, pct},
	WarmUpsSinceCodesCleared:    {"Warm-ups since codes cleared", "", 1, a},
	DistanceSinceCodesCleared:   {"Distance traveled since codes cleared", "km", 2, u16},
	EvapVaporPressure:           {"Evap. system vapor pressure", "Pa", 2, func(d []byte) float64 { return float64(int16(uint16(d[0])<<8|uint16(d[1]))) / 4 }},
	BarometricPressure:          {"Absolute barometric pressure", "kPa", 1, a},
	CatalystTempBank1Sensor1:    {"Catalyst temperature, bank 1 sensor 1", "°C", 2, catalyst},
	CatalystTempBank2Sensor1:    {"Catalyst temperature, bank 2 sensor 1", "°C", 2, catalyst},
	CatalystTempBank1Sensor2:    {"Catalyst temperature, bank 1 sensor 2", "°C", 2, catalyst},
	CatalystTempBank2Sensor2:    {"Catalyst temperature, bank 2 sensor 2", "°C", 2, catalyst},
	MonitorStatusThisDriveCycle: {Name: "Monitor status this drive cycle", Size: 4},
	ControlModuleVoltage:        {"Control module voltage", "V", 2, func(d []byte) float64 { return u16(d) / 1000 }},
	AbsoluteLoad:                {"Absolute load value", "%", 2, func(d []byte) float64 { return u16(d) * 100 / 255 }},
	CommandedEquivalenceRatio:   {"Commanded air-fuel equivalence ratio", "λ", 2, func(d []byte) float64 { return u16(d) * 2 / 65536 }},
	RelativeThrottlePosition:    {"Relative throttle position", "%", 1, pct},
	AmbientAirTemp:              {"Ambient air temperature", "°C", 1, temp},
	AbsoluteThrottlePositionB:   {"Absolute throttle position B", "%", 1, pct},
	AbsoluteThrottlePositionC:   {"Absolute throttle position C", "%", 1, pct},
	AcceleratorPedalPositionD:   {"Accelerator pedal position D", "%", 1, pct},
	AcceleratorPedalPositionE:   {"Accelerator pedal position E", "%", 1, pct},
	AcceleratorPedalPositionF:   {"Accelerator pedal position F", "%", 1, pct},
	CommandedThrottleActuator:   {"Commanded throttle actuator", "%", 1, pct},
	TimeRunWithMIL:              {"Time run with MIL on", "min", 2, u16},
	TimeSinceCodesCleared:       {"Time since trouble codes cleared", "min", 2, u16},
	MaximumValues:               {Name: "Maximum values for equivalence ratio, O2 voltage, O2 current and MAP", Size: 4},
	MaximumMAFAirFlowRate:       {"Maximum value for mass air flow rate", "g/s", 4, func(d []byte) float64 { return 10 * a(d) }},
	FuelType:                    {Name: "Fuel type", Size: 1},
	EthanolFuelPercent:          {"Ethanol fuel percentage", "%", 1, pct},
	AbsoluteEvapVaporPressure:   {"Absolute evap. system vapor pressure", "kPa", 2, func(d []byte) float64 { return u16(d) / 200 }},
	EvapVaporPressureWide:       {"Evap. system vapor pressure", "Pa", 2, func(d []byte) float64 { return u16(d) - 32767 }},
	ShortTermSecondaryO2Trim13:  {Name: "Short term secondary oxygen sensor trim, banks 1 and 3", Size: 2},
	LongTermSecondaryO2Trim13:   {Name: "Long term secondary oxygen sensor trim, banks 1 and 3", Size: 2},
	ShortTermSecondaryO2Trim24:  {Name: "Short term secondary oxygen sensor trim, banks 2 and 4", Size: 2},
	LongTermSecondaryO2Trim24:   {Name: "Long term secondary oxygen sensor trim, banks 2 and 4", Size: 2},
	FuelRailAbsolutePressure:    {"Fuel rail absolute pressure", "kPa", 2, func(d []byte) float64 { return 10 * u16(d) }},
	RelativeAcceleratorPosition: {"Relative accelerator pedal position", "%", 1, pct},
	HybridBatteryRemainingLife:  {"Hybrid battery pack remaining life", "%", 1, pct},
	EngineOilTemp:               {"Engine oil temperature", "°C", 1, temp},
	FuelInjectionTiming:         {"Fuel injection timing", "°", 2, func(d []byte) float64 { return u16(d)/128 - 210 }},
	EngineFuelRate:              {"Engine fuel rate", "L/h", 2, func(d []byte) float64 { return u16(d) / 20 }},
	EmissionRequirements:        {Name: "Emission requirements the vehicle is designed to", Size: 1},
	DriverDemandTorque:          {"Driver's demand engine percent torque", "%", 1, func(d []byte) float64 { return a(d) - 125 }},
	ActualEngineTorque:          {"Actual engine percent torque", "%", 1, func(d []byte) float64 { return a(d) - 125 }},
	EngineReferenceTorque:       {"Engine reference torque", "N·m", 2, u16},
	Odometer: {"Odometer", "km", 4, func(d []byte) float64 {
		return float64(uint32(d[0])<<24|uint32(d[1])<<16|uint32(d[2])<<8|uint32(d[3])) / 10
	}},
}

func catalyst(d []byte) float64 { return u16(d)/10 - 40 }

func init() {
	for base := PID(0x00); base <= 0xC0; base += 0x20 {
		pidTable[base] = Info{
			Name: fmt.Sprintf("PIDs supported [%02X-%02X]", byte(base)+1, byte(base)+0x20),
			Size: 4,
		}
	}
	for i := range PID(8) {
		pidTable[O2Sensor1+i] = Info{Name: fmt.Sprintf("Oxygen sensor %d (voltage, short term fuel trim)", i+1), Size: 2}
		pidTable[O2Sensor1RatioVoltage+i] = Info{Name: fmt.Sprintf("Oxygen sensor %d (equivalence ratio, voltage)", i+1), Size: 4}
		pidTable[O2Sensor1RatioCurrent+i] = Info{Name: fmt.Sprintf("Oxygen sensor %d (equivalence ratio, current)", i+1), Size: 4}
	}
}

// Reading is the value of one PID as reported by one ECU.
type Reading struct {
	PID  PID
	ECU  uint32
	Data []byte // the data bytes that follow the PID in the response
}

// Float decodes the reading into a number in the unit its Info gives. It
// fails for PIDs that are not Numeric; read their bytes from Data.
func (r Reading) Float() (float64, error) {
	info, ok := r.PID.Info()
	if !ok || info.decode == nil {
		return 0, fmt.Errorf("obd2: %v has no numeric value", r.PID)
	}
	if len(r.Data) < info.Size {
		return 0, fmt.Errorf("obd2: %v: got %d data bytes, want %d", r.PID, len(r.Data), info.Size)
	}
	return info.decode(r.Data), nil
}

// String formats the reading, e.g. "Engine speed: 1726 rpm".
func (r Reading) String() string {
	info, _ := r.PID.Info()
	if v, err := r.Float(); err == nil {
		if info.Unit == "" {
			return fmt.Sprintf("%v: %g", r.PID, v)
		}
		return fmt.Sprintf("%v: %g %s", r.PID, v, info.Unit)
	}
	return fmt.Sprintf("%v: % X", r.PID, r.Data)
}

// Readings is the result of Client.Query.
type Readings []Reading

// Get returns the first reading of p. When several ECUs report the same
// PID, the ECU with the lowest identifier comes first.
func (rs Readings) Get(p PID) (Reading, bool) {
	for _, r := range rs {
		if r.PID == p {
			return r, true
		}
	}
	return Reading{}, false
}

// Float decodes the first reading of p.
func (rs Readings) Float(p PID) (float64, error) {
	r, ok := rs.Get(p)
	if !ok {
		return 0, fmt.Errorf("%w for %v", ErrNoResponse, p)
	}
	return r.Float()
}

// parseReadings splits the data of a service 01 response, after its service
// ID, into readings. A response to a multi-PID request holds several PID and
// data groups, sized by the PID table. When single is true the request asked
// for one PID, so all the data belongs to it, even bytes beyond its usual
// size.
func parseReadings(ecu uint32, data []byte, single bool) ([]Reading, error) {
	var out []Reading
	for len(data) > 0 {
		pid := PID(data[0])
		data = data[1:]
		n := len(data)
		if info, ok := pid.Info(); ok && !single {
			n = info.Size
		}
		if n > len(data) {
			return out, fmt.Errorf("obd2: %v: response has %d data bytes, want %d", pid, len(data), n)
		}
		out = append(out, Reading{PID: pid, ECU: ecu, Data: append([]byte(nil), data[:n]...)})
		data = data[n:]
	}
	return out, nil
}

// supportedFromBitmap returns the PIDs announced by the bitmap that a
// "PIDs supported" PID returns. Bit 7 of the first byte stands for base+1.
func supportedFromBitmap(base PID, bitmap []byte) []PID {
	var out []PID
	for i := 0; i < 32 && i/8 < len(bitmap); i++ {
		if bitmap[i/8]&(0x80>>(i%8)) != 0 {
			out = append(out, base+PID(i)+1)
		}
	}
	return out
}
