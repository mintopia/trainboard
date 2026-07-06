package obs

// FaultCode is a short diagnostic code surfaced in a corner of the screen
// during Error / ClockNotSynced scenes for field diagnosis.
type FaultCode string

// The M1 fault-code registry (spec §Observability).
const (
	FaultNone              FaultCode = ""
	FaultDarwinUnreachable FaultCode = "E01"
	FaultAuthRejected      FaultCode = "E02"
	FaultClockNotSynced    FaultCode = "E03"
	FaultConfigError       FaultCode = "E04"
)

// Message returns the short operator-facing description of the fault.
func (f FaultCode) Message() string {
	switch f {
	case FaultDarwinUnreachable:
		return "Darwin unreachable"
	case FaultAuthRejected:
		return "Darwin token rejected"
	case FaultClockNotSynced:
		return "Waiting for time sync"
	case FaultConfigError:
		return "Configuration error"
	default:
		return ""
	}
}
