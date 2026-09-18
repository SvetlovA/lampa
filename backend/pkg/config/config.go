// Package config loads lampa-api settings from LAMPA_API_* environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// environment variable names
const (
	envListen       = "LAMPA_API_LISTEN"
	envHealthListen = "LAMPA_API_HEALTH_LISTEN"
	envDBDSN        = "LAMPA_API_DB_DSN"
	envDataKey      = "LAMPA_API_DATA_KEY"
	envMaxBodyBytes = "LAMPA_API_MAX_BODY_BYTES"
)

// defaults for optional variables
const (
	defaultListen       = ":8080"
	defaultHealthListen = ":8081"
	defaultMaxBodyBytes = 2 << 20 // 2 MiB
)

// DataKeySize is the required length of the decoded data encryption key (AES-256).
const DataKeySize = 32

// ErrMissing is returned when a required variable is unset or empty.
var ErrMissing = errors.New("required variable is not set")

// ErrInvalid is returned when a variable is set to an unusable value.
var ErrInvalid = errors.New("invalid value")

// Config holds validated service settings. DBDSN and DataKey are secrets and never printed.
type Config struct {
	Listen       string            // public API listen address, host:port
	HealthListen string            // health listen address, host:port, differs from Listen
	DBDSN        string            // postgres connection string, secret
	DataKey      [DataKeySize]byte // AES-256 key sealing connection credentials, secret
	MaxBodyBytes int64             // request body limit in bytes, > 0
}

// Load reads and validates the configuration through lookup (os.LookupEnv in main).
// empty values are treated as unset, errors name the variable but never include its value.
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(name string) string {
		v, _ := lookup(name)
		return v
	}

	cfg := Config{
		Listen:       withDefault(get(envListen), defaultListen),
		HealthListen: withDefault(get(envHealthListen), defaultHealthListen),
		DBDSN:        get(envDBDSN),
		MaxBodyBytes: defaultMaxBodyBytes,
	}

	if err := validateAddr(cfg.Listen); err != nil {
		return Config{}, fmt.Errorf("%s: %w", envListen, err)
	}
	if err := validateAddr(cfg.HealthListen); err != nil {
		return Config{}, fmt.Errorf("%s: %w", envHealthListen, err)
	}
	if cfg.Listen == cfg.HealthListen {
		return Config{}, fmt.Errorf("%s: must differ from %s: %w", envHealthListen, envListen, ErrInvalid)
	}

	if cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("%s: %w", envDBDSN, ErrMissing)
	}

	key, err := decodeKey(get(envDataKey))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", envDataKey, err)
	}
	cfg.DataKey = key

	if v := get(envMaxBodyBytes); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("%s: must be a positive integer: %w", envMaxBodyBytes, ErrInvalid)
		}
		cfg.MaxBodyBytes = n
	}

	return cfg, nil
}

// String returns a printable form with the DSN and data key redacted.
func (c Config) String() string {
	return fmt.Sprintf("{Listen:%s HealthListen:%s DBDSN:%s DataKey:%s MaxBodyBytes:%d}",
		c.Listen, c.HealthListen, redact(c.DBDSN != ""), redact(c.DataKey != [DataKeySize]byte{}), c.MaxBodyBytes)
}

// GoString keeps %#v redacted as well.
func (c Config) GoString() string {
	return "config.Config" + c.String()
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// validateAddr accepts host:port with an empty or named host and a numeric port.
func validateAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be host:port: %w", ErrInvalid)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("port must be 0-65535: %w", ErrInvalid)
	}
	return nil
}

// decodeKey decodes standard base64 into exactly DataKeySize bytes.
func decodeKey(v string) ([DataKeySize]byte, error) {
	var key [DataKeySize]byte
	if v == "" {
		return key, ErrMissing
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		// the decoder error only carries a byte offset, not the value
		return key, fmt.Errorf("decode base64: %w", errors.Join(ErrInvalid, err))
	}
	if len(raw) != DataKeySize {
		return key, fmt.Errorf("must decode to %d bytes, got %d: %w", DataKeySize, len(raw), ErrInvalid)
	}
	copy(key[:], raw)
	return key, nil
}

func redact(set bool) string {
	if set {
		return "[redacted]"
	}
	return "[unset]"
}
