package obs

import "testing"

func TestFaultMessages(t *testing.T) {
	cases := map[FaultCode]string{
		FaultNone:              "",
		FaultDarwinUnreachable: "Darwin unreachable",
		FaultAuthRejected:      "Darwin token rejected",
		FaultClockNotSynced:    "Waiting for time sync",
		FaultConfigError:       "Configuration error",
	}
	for code, want := range cases {
		if got := code.Message(); got != want {
			t.Errorf("%q.Message() = %q, want %q", code, got, want)
		}
	}
}

func TestM3FaultCodes(t *testing.T) {
	if FaultRadioBlocked != "E05" || FaultRadioBlocked.Message() != "WiFi radio blocked" {
		t.Fatalf("E05 wrong: %q %q", FaultRadioBlocked, FaultRadioBlocked.Message())
	}
	if FaultConnectivity != "E06" || FaultConnectivity.Message() != "Network connectivity" {
		t.Fatalf("E06 wrong: %q %q", FaultConnectivity, FaultConnectivity.Message())
	}
}

func TestFaultUpdateRecovery(t *testing.T) {
	if FaultUpdateRecovery != "E07" {
		t.Errorf("FaultUpdateRecovery = %q, want E07", FaultUpdateRecovery)
	}
	if got := FaultUpdateRecovery.Message(); got != "Update recovery mode" {
		t.Errorf("Message() = %q", got)
	}
}
