package net

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckEvaluateAllProbesPassIsStageOK(t *testing.T) {
	c := NewCheckWithProbes(Probes{
		Assoc:   func(context.Context) error { return nil },
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	stage, err := c.Evaluate(context.Background())
	if stage != StageOK {
		t.Fatalf("stage = %q, want StageOK", stage)
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCheckEvaluateShortCircuitsOnFirstFailure(t *testing.T) {
	tests := []struct {
		name        string
		failAssoc   bool
		failDHCP    bool
		failDNS     bool
		failCaptive bool
		wantStage   Stage
	}{
		{name: "assoc fails first", failAssoc: true, wantStage: StageAssoc},
		{name: "dhcp fails first", failDHCP: true, wantStage: StageDHCP},
		{name: "dns fails first", failDNS: true, wantStage: StageDNS},
		{name: "captive fails first", failCaptive: true, wantStage: StageCaptive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var assocCalled, dhcpCalled, dnsCalled, captiveCalled bool
			wantErr := errors.New("boom")

			probe := func(called *bool, shouldFail bool) func(context.Context) error {
				return func(context.Context) error {
					*called = true
					if shouldFail {
						return wantErr
					}
					return nil
				}
			}

			c := NewCheckWithProbes(Probes{
				Assoc:   probe(&assocCalled, tt.failAssoc),
				DHCP:    probe(&dhcpCalled, tt.failDHCP),
				DNS:     probe(&dnsCalled, tt.failDNS),
				Captive: probe(&captiveCalled, tt.failCaptive),
			})

			stage, err := c.Evaluate(context.Background())
			if stage != tt.wantStage {
				t.Fatalf("stage = %q, want %q", stage, tt.wantStage)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v, want %v", err, wantErr)
			}

			if !assocCalled {
				t.Fatal("assoc probe should always be called")
			}
			if tt.wantStage == StageAssoc {
				if dhcpCalled || dnsCalled || captiveCalled {
					t.Fatal("later probes must not run after assoc failure")
				}
				return
			}
			if !dhcpCalled {
				t.Fatal("dhcp probe should be called")
			}
			if tt.wantStage == StageDHCP {
				if dnsCalled || captiveCalled {
					t.Fatal("later probes must not run after dhcp failure")
				}
				return
			}
			if !dnsCalled {
				t.Fatal("dns probe should be called")
			}
			if tt.wantStage == StageDNS {
				if captiveCalled {
					t.Fatal("later probes must not run after dns failure")
				}
				return
			}
			if !captiveCalled {
				t.Fatal("captive probe should be called")
			}
		})
	}
}

func TestNewCheckAssocProbe(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantErr bool
		errText string
	}{
		{name: "completed passes", out: "wpa_state=COMPLETED\n", wantErr: false},
		{name: "scanning fails with state in error", out: "wpa_state=SCANNING\n", wantErr: true, errText: "SCANNING"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewFakeRunner()
			r.Script("wpa_cli -i wlan0 status", tt.out, nil)

			c := NewCheck(r, "wlan0", "localhost", "http://connectivitycheck.gstatic.com/generate_204",
				func(context.Context, string) (int, string, error) { return 204, "", nil })

			stage, err := c.Evaluate(context.Background())
			if tt.wantErr {
				if stage != StageAssoc {
					t.Fatalf("stage = %q, want StageAssoc", stage)
				}
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("err = %v, want containing %q", err, tt.errText)
				}
				return
			}
			if stage == StageAssoc {
				t.Fatalf("assoc should pass, got stage %q err %v", stage, err)
			}
		})
	}
}

func TestNewCheckDHCPProbe(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantErr bool
	}{
		{name: "has inet passes", out: "3: wlan0    inet 192.168.3.181/24 brd 192.168.3.255 scope global wlan0\n", wantErr: false},
		{name: "no inet fails", out: "3: wlan0    <BROADCAST,MULTICAST> mtu 1500 qdisc noqueue state UP\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewFakeRunner()
			r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
			r.Script("ip -4 addr show dev wlan0", tt.out, nil)

			c := NewCheck(r, "wlan0", "localhost", "http://connectivitycheck.gstatic.com/generate_204",
				func(context.Context, string) (int, string, error) { return 204, "", nil })

			stage, err := c.Evaluate(context.Background())
			if tt.wantErr {
				if stage != StageDHCP {
					t.Fatalf("stage = %q, want StageDHCP", stage)
				}
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if stage == StageDHCP {
				t.Fatalf("dhcp should pass, got stage %q err %v", stage, err)
			}
		})
	}
}

func TestNewCheckCaptiveProbe(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		httpErr    error
		wantErr    bool
		wantStatus bool // wants status value named in the error text
	}{
		{name: "204 passes", status: 204, wantErr: false},
		{name: "302 is captive trap", status: 302, wantErr: true, wantStatus: true},
		{name: "transport error fails", httpErr: errors.New("dial tcp: connection refused"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewFakeRunner()
			r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
			r.Script("ip -4 addr show dev wlan0", "3: wlan0    inet 192.168.3.181/24 scope global wlan0\n", nil)

			c := NewCheck(r, "wlan0", "localhost", "http://connectivitycheck.gstatic.com/generate_204",
				func(context.Context, string) (int, string, error) { return tt.status, "", tt.httpErr })

			stage, err := c.Evaluate(context.Background())
			if tt.wantErr {
				if stage != StageCaptive {
					t.Fatalf("stage = %q, want StageCaptive", stage)
				}
				if err == nil {
					t.Fatal("want error")
				}
				if tt.wantStatus && !strings.Contains(err.Error(), "302") {
					t.Fatalf("err = %v, want containing status 302", err)
				}
				return
			}
			if stage != StageOK {
				t.Fatalf("stage = %q, want StageOK, err %v", stage, err)
			}
		})
	}
}
