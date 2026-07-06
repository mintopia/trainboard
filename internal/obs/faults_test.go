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
