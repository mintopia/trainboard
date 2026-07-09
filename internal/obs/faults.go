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
	// FaultRadioBlocked: wlan0 is rfkill-soft-blocked or the regulatory
	// domain is unset — AP mode would be dead-on-arrival (M3 spec, issue #6).
	FaultRadioBlocked FaultCode = "E05"
	// FaultConnectivity: a layered connectivity stage failed (association /
	// DHCP / DNS / captive); the failing stage is carried on the Snapshot.
	FaultConnectivity FaultCode = "E06"
	// FaultUpdateRecovery: the launcher hit a double fault (known-good slot
	// itself failing) and exec'd the payload in --recovery mode — web UI +
	// AP only, no departures (M5 spec §2, issue #18).
	FaultUpdateRecovery FaultCode = "E07"
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
	case FaultRadioBlocked:
		return "WiFi radio blocked"
	case FaultConnectivity:
		return "Network connectivity"
	case FaultUpdateRecovery:
		return "Update recovery mode"
	default:
		return ""
	}
}
