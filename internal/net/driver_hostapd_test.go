package net

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func newTestHostapdDriver(r Runner) (*hostapdDriver, *fakeFileWriter, *fakeSleeper) {
	fw := &fakeFileWriter{}
	sl := &fakeSleeper{}
	d := newHostapdDriver(r, "wlan0", "GB", fw.write, sl.sleep)
	return d, fw, sl
}

// (a) StartAP happy path issues exactly the expected argv sequence in order.
func TestHostapdDriverStartAPHappyPathIssuesExactSequence(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 disable_network 0", "", nil)
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
	r.Script("hostapd -B /run/trainboard-hostapd.conf", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)
	r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

	d, fw, _ := newTestHostapdDriver(r)

	err := d.StartAP(context.Background(), APConfig{
		SSID: "Trainboard-1234",
		Addr: "192.168.4.1/24",
	})
	if err != nil {
		t.Fatalf("StartAP() = %v, want nil", err)
	}

	want := []string{
		"wpa_cli -i wlan0 disable_network 0",        // release the iface from wpa_supplicant's STA control
		"pkill -F " + dhclientPidfile + " dhclient", // issue #46: kill the STA dhclient daemon
		"hostapd -B /run/trainboard-hostapd.conf",
		"ip addr flush dev wlan0",
		"ip addr add 192.168.4.1/24 dev wlan0",
	}
	got := r.Calls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() =\n%v\nwant\n%v", got, want)
	}

	if len(fw.writes()) != 1 {
		t.Fatalf("writeFile called %d times, want 1", len(fw.writes()))
	}
	if fw.writes()[0].path != hostapdConfPath {
		t.Fatalf("write path = %q, want %q", fw.writes()[0].path, hostapdConfPath)
	}
}

// (b) StartAP tolerates disable_network 0 failing (STA network may not be
// running/exist yet) and still proceeds to launch hostapd.
func TestHostapdDriverStartAPTeleratesDisableNetworkFailure(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 disable_network 0", "", errors.New("exit status 1"))
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
	r.Script("hostapd -B /run/trainboard-hostapd.conf", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)
	r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

	d, _, _ := newTestHostapdDriver(r)

	err := d.StartAP(context.Background(), APConfig{
		SSID: "Trainboard-1234",
		Addr: "192.168.4.1/24",
	})
	if err != nil {
		t.Fatalf("StartAP() = %v, want nil (disable_network failure must be tolerated)", err)
	}
}

// (c) hostapd conf content: template fields substituted; SSID/passphrase
// containing a newline are rejected before write (hostapd.conf has no
// escaping, so a newline is a config-injection vector).
func TestHostapdDriverConfWriteSubstitutionAndNewlineRejection(t *testing.T) {
	t.Run("conf content matches template with substitution", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 disable_network 0", "", nil)
		r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
		r.Script("hostapd -B /run/trainboard-hostapd.conf", "", nil)
		r.Script("ip addr flush dev wlan0", "", nil)
		r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

		d, fw, _ := newTestHostapdDriver(r)

		err := d.StartAP(context.Background(), APConfig{
			SSID: "Trainboard-ABCD",
			Addr: "192.168.4.1/24",
		})
		if err != nil {
			t.Fatalf("StartAP() = %v, want nil", err)
		}

		writes := fw.writes()
		if len(writes) != 1 {
			t.Fatalf("writeFile called %d times, want 1", len(writes))
		}
		conf := string(writes[0].data)

		for _, want := range []string{
			"interface=wlan0",
			"driver=nl80211",
			"ssid=Trainboard-ABCD",
			"country_code=GB",
			"hw_mode=g",
			"channel=6",
		} {
			if !strings.Contains(conf, want) {
				t.Errorf("conf missing %q; conf:\n%s", want, conf)
			}
		}
		// The AP is now open (issue #44): every WPA/passphrase directive is
		// gone, so hostapd brings the AP up with no encryption.
		for _, notWant := range []string{"wpa=2", "wpa_key_mgmt", "rsn_pairwise", "wpa_passphrase"} {
			if strings.Contains(conf, notWant) {
				t.Errorf("open AP conf must not contain %q; conf:\n%s", notWant, conf)
			}
		}
	})

	t.Run("SSID containing a newline is rejected, not written", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 disable_network 0", "", nil)

		d, fw, _ := newTestHostapdDriver(r)

		err := d.StartAP(context.Background(), APConfig{
			SSID: "Trainboard\ninterface=evil0",
			Addr: "192.168.4.1/24",
		})
		if err == nil {
			t.Fatal("StartAP() = nil, want error for newline-containing SSID")
		}
		if len(fw.writes()) != 0 {
			t.Fatalf("writeFile called %d times, want 0 (newline must be rejected before write)", len(fw.writes()))
		}
	})

	t.Run("configured country is used instead of a hardcoded GB", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 disable_network 0", "", nil)
		r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
		r.Script("hostapd -B /run/trainboard-hostapd.conf", "", nil)
		r.Script("ip addr flush dev wlan0", "", nil)
		r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

		fw := &fakeFileWriter{}
		sl := &fakeSleeper{}
		d := newHostapdDriver(r, "wlan0", "US", fw.write, sl.sleep)

		err := d.StartAP(context.Background(), APConfig{
			SSID: "Trainboard-ABCD",
			Addr: "192.168.4.1/24",
		})
		if err != nil {
			t.Fatalf("StartAP() = %v, want nil", err)
		}

		conf := string(fw.writes()[0].data)
		if !strings.Contains(conf, "country_code=US") {
			t.Fatalf("conf missing %q; conf:\n%s", "country_code=US", conf)
		}
		if strings.Contains(conf, "country_code=GB") {
			t.Fatalf("conf hardcodes country_code=GB instead of the configured country; conf:\n%s", conf)
		}
	})

	t.Run("SSID containing a quote is rejected, not written", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 disable_network 0", "", nil)

		d, fw, _ := newTestHostapdDriver(r)

		err := d.StartAP(context.Background(), APConfig{
			SSID: `Trainboard"ABCD`,
			Addr: "192.168.4.1/24",
		})
		if err == nil {
			t.Fatal("StartAP() = nil, want error for quote-containing SSID")
		}
		if len(fw.writes()) != 0 {
			t.Fatalf("writeFile called %d times, want 0 (quote must be rejected before write)", len(fw.writes()))
		}
	})
}

// (d) StopAP happy path: pkill then ip addr flush.
func TestHostapdDriverStopAPHappyPathIssuesExactSequence(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -x hostapd", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)

	d, _, _ := newTestHostapdDriver(r)

	err := d.StopAP(context.Background())
	if err != nil {
		t.Fatalf("StopAP() = %v, want nil", err)
	}

	want := []string{
		"pkill -x hostapd",
		"ip addr flush dev wlan0",
	}
	got := r.Calls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() =\n%v\nwant\n%v", got, want)
	}
}

// (e) StopAP tolerates pkill exit 1 (no matching hostapd process running).
func TestHostapdDriverStopAPTeleratesPkillNoProcessRunning(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -x hostapd", "", errors.New("exit status 1"))
	r.Script("ip addr flush dev wlan0", "", nil)

	d, _, _ := newTestHostapdDriver(r)

	err := d.StopAP(context.Background())
	if err != nil {
		t.Fatalf("StopAP() = %v, want nil (pkill exit 1 must be tolerated)", err)
	}

	got := r.Calls()
	if len(got) == 0 || got[len(got)-1] != "ip addr flush dev wlan0" {
		t.Fatalf("Calls() = %v, want ip addr flush to still run after pkill failure", got)
	}
}

// (f) AttemptSTA happy path stops the AP first, then runs the shared
// wpa_cli/dhclient flow ending with the daemon-mode `dhclient -v -pf
// <pidfile> wlan0`.
func TestHostapdDriverAttemptSTAHappyPathStopsAPThenEndsWithDHClient(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -x hostapd", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -v -pf "+dhclientPidfile+" wlan0", "bound to 192.168.3.181\n", nil)

	d, fw, _ := newTestHostapdDriver(r)

	err := d.AttemptSTA(context.Background(), STAConfig{SSID: "HomeWifi", PSK: "supersecretpsk"})
	if err != nil {
		t.Fatalf("AttemptSTA() = %v, want nil", err)
	}

	want := []string{
		"pkill -x hostapd",
		"ip addr flush dev wlan0",
		"pkill -F " + dhclientPidfile + " dhclient", // issue #46: kill-before-start (staAttempt)
		"wpa_cli -i wlan0 reconfigure",
		"wpa_cli -i wlan0 select_network 0",
		"wpa_cli -i wlan0 status",
		"dhclient -v -pf " + dhclientPidfile + " wlan0",
	}
	got := r.Calls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() =\n%v\nwant\n%v", got, want)
	}
	if len(fw.writes()) != 1 {
		t.Fatalf("writeFile called %d times, want 1", len(fw.writes()))
	}
}

// (g) AttemptSTA surfaces dhclient failure.
func TestHostapdDriverAttemptSTASurfacesDHClientFailure(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -x hostapd", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -v -pf "+dhclientPidfile+" wlan0", "No DHCPOFFERS received.\n", errors.New("exit status 2"))

	d, _, _ := newTestHostapdDriver(r)

	err := d.AttemptSTA(context.Background(), STAConfig{SSID: "HomeWifi", PSK: "supersecretpsk"})
	if err == nil {
		t.Fatal("AttemptSTA() = nil, want error")
	}
	if !strings.Contains(err.Error(), "dhclient") {
		t.Fatalf("err = %v, want containing %q", err, "dhclient")
	}
}

// (h) APActive true/false via scripted pgrep.
func TestHostapdDriverAPActive(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		err     error
		want    bool
		wantErr bool
	}{
		{name: "pgrep finds hostapd (exit 0)", out: "1234\n", err: nil, want: true},
		{name: "pgrep finds no process (nonzero exit)", out: "", err: errors.New("exit status 1"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewFakeRunner()
			r.Script("pgrep -x hostapd", tt.out, tt.err)

			d, _, _ := newTestHostapdDriver(r)

			got, err := d.APActive(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("APActive() err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("APActive() err = %v, want nil", err)
			}
			if got != tt.want {
				t.Fatalf("APActive() = %v, want %v", got, tt.want)
			}
		})
	}
}
