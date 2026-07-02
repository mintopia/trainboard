package config

import "fmt"

const redacted = "***REDACTED***"

// Redacted returns a copy of c with the Darwin token masked (empty stays empty).
func (c Config) Redacted() Config {
	if c.Darwin.Token != "" {
		c.Darwin.Token = redacted
	}
	return c
}

// String renders the config with the token masked, safe for logs.
func (c Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("Config{version:%d origin:%q dest:%q services:%d refresh:%ds darwin:%s powersaving:%t}",
		r.Version, r.Board.Origin, r.Board.Destination, r.Board.Services,
		r.Board.RefreshSeconds, r.Darwin, r.Powersaving.Enabled)
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
