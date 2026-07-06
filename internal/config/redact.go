package config

import "fmt"

const redacted = "***REDACTED***"

// Redacted returns a copy of c with the Darwin token, wifi PSK, provisioning
// AP password, and web password hash masked (empty stays empty).
func (c Config) Redacted() Config {
	if c.Darwin.Token != "" {
		c.Darwin.Token = redacted
	}
	if c.Wifi.PSK != "" {
		c.Wifi.PSK = redacted
	}
	if c.Provisioning.APPassword != "" {
		c.Provisioning.APPassword = redacted
	}
	if c.Web.PasswordHash != "" {
		c.Web.PasswordHash = redacted
	}
	return c
}

// String renders the config with all secrets masked, safe for logs.
func (c Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("Config{version:%d origin:%q dest:%q services:%d refresh:%ds darwin:%s powersaving:%t wifi:%s provisioning:%s}",
		r.Version, r.Board.Origin, r.Board.Destination, r.Board.Services,
		r.Board.RefreshSeconds, r.Darwin, r.Powersaving.Enabled, r.Wifi, r.Provisioning)
}

// String masks the token so DarwinConfig can't leak it via %s/%v.
func (d DarwinConfig) String() string {
	if d.Token == "" {
		return "DarwinConfig{token:unset}"
	}
	return "DarwinConfig{token:" + redacted + "}"
}

// GoString masks the token so %#v (fmt.GoStringer) can't leak it either.
func (c Config) GoString() string { return c.String() }

// GoString masks the token so %#v can't leak it.
func (d DarwinConfig) GoString() string { return d.String() }

// String masks the PSK so WifiConfig can't leak it via %s/%v; SSID is not a
// secret and passes through.
func (w WifiConfig) String() string {
	if w.PSK == "" {
		return fmt.Sprintf("WifiConfig{ssid:%q psk:unset}", w.SSID)
	}
	return fmt.Sprintf("WifiConfig{ssid:%q psk:%s}", w.SSID, redacted)
}

// GoString masks the PSK so %#v can't leak it.
func (w WifiConfig) GoString() string { return w.String() }

// String masks the AP password so ProvisioningConfig can't leak it via %s/%v.
func (p ProvisioningConfig) String() string {
	if p.APPassword == "" {
		return "ProvisioningConfig{apPassword:unset}"
	}
	return "ProvisioningConfig{apPassword:" + redacted + "}"
}

// GoString masks the AP password so %#v can't leak it.
func (p ProvisioningConfig) GoString() string { return p.String() }
