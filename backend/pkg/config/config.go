// Package config loads lampa-api settings from layered appsettings files and the environment.
//
// appsettings.json holds the shared defaults and appsettings.<Environment>.json, when present,
// overrides some of them. secrets are {ENV_VAR} placeholders resolved from the environment.
package config

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// EnvEnvironment names the variable selecting the environment; unset or empty means Test.
const EnvEnvironment = "LAMPA_ENVIRONMENT"

// supported environments, each may have its own appsettings.<Environment>.json
const (
	Development = "Development"
	Test        = "Test"
	Production  = "Production"
)

// DataKeySize is the required length of the decoded data encryption key (AES-256).
const DataKeySize = 32

const baseFile = "appsettings.json"

// ErrMissing is returned when a required setting or variable is unset or empty.
var ErrMissing = errors.New("required value is not set")

// ErrInvalid is returned when a setting or variable has an unusable value.
var ErrInvalid = errors.New("invalid value")

//go:embed defaults/appsettings*.json
var embedded embed.FS

// Defaults holds the shipped appsettings files at its root.
var Defaults = mustSub(embedded, "defaults")

// mustSub returns the dir subtree of fsys and panics on an invalid dir path.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

var (
	environments  = []string{Development, Test, Production}
	sslModes      = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
	placeholderRe = regexp.MustCompile(`\{([A-Z][A-Z0-9_]*)\}`)
)

// Config holds validated service settings. DBDSN and DataKey are secrets and never printed.
type Config struct {
	Environment  string            // one of Development, Test, Production
	Listen       string            // public API listen address, host:port
	HealthListen string            // health listen address, host:port, differs from Listen
	DBDSN        string            // postgres connection string, secret
	DataKey      [DataKeySize]byte // AES-256 key sealing connection credentials, secret
	MaxBodyBytes int64             // request body limit in bytes, > 0
}

// settings mirrors the appsettings files; every layer decodes into the same value.
type settings struct {
	API struct {
		Listen       string `json:"Listen"`
		MaxBodyBytes int64  `json:"MaxBodyBytes"`
	} `json:"Api"`
	Health struct {
		Listen string `json:"Listen"`
	} `json:"Health"`
	Database struct {
		Host     string `json:"Host"`
		Port     int    `json:"Port"`
		Name     string `json:"Name"`
		User     string `json:"User"`
		Password string `json:"Password"`
		SSLMode  string `json:"SSLMode"`
	} `json:"Database"`
	DataKey string `json:"DataKey"`
}

// Load reads appsettings.json and the optional appsettings.<Environment>.json from fsys
// (Defaults in main), resolves placeholders through lookup (os.LookupEnv in main) and validates
// the result. errors name the setting or variable but never include a secret value.
func Load(fsys fs.FS, lookup func(string) (string, bool)) (Config, error) {
	env, err := environment(lookup)
	if err != nil {
		return Config{}, err
	}
	s, err := readSettings(fsys, env)
	if err != nil {
		return Config{}, err
	}

	// fixed order: the password is resolved and reported before the data key
	s.Database.Password, err = resolve("Database.Password", s.Database.Password, lookup)
	if err != nil {
		return Config{}, err
	}
	s.DataKey, err = resolve("DataKey", s.DataKey, lookup)
	if err != nil {
		return Config{}, err
	}

	key, err := s.validate()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Environment:  env,
		Listen:       s.API.Listen,
		HealthListen: s.Health.Listen,
		DBDSN:        s.dsn(),
		DataKey:      key,
		MaxBodyBytes: s.API.MaxBodyBytes,
	}, nil
}

// String returns a printable form with the DSN and data key redacted.
func (c Config) String() string {
	return fmt.Sprintf("{Environment:%s Listen:%s HealthListen:%s DBDSN:%s DataKey:%s MaxBodyBytes:%d}",
		c.Environment, c.Listen, c.HealthListen, redact(c.DBDSN != ""), redact(c.DataKey != [DataKeySize]byte{}),
		c.MaxBodyBytes)
}

// GoString keeps %#v redacted as well.
func (c Config) GoString() string {
	return "config.Config" + c.String()
}

// environment returns the selected environment, Test when the variable is unset or empty.
func environment(lookup func(string) (string, bool)) (string, error) {
	env, _ := lookup(EnvEnvironment)
	if env == "" {
		return Test, nil
	}
	if !slices.Contains(environments, env) {
		return "", fmt.Errorf("%s: must be one of %s: %w", EnvEnvironment, strings.Join(environments, ", "), ErrInvalid)
	}
	return env, nil
}

// readSettings decodes the base file, then the optional file of env over it.
func readSettings(fsys fs.FS, env string) (settings, error) {
	var s settings
	if err := decodeFile(fsys, baseFile, &s, true); err != nil {
		return settings{}, err
	}
	if err := decodeFile(fsys, "appsettings."+env+".json", &s, false); err != nil {
		return settings{}, err
	}
	return s, nil
}

// decodeFile strictly decodes the named file over s, so keys absent from the file keep their
// current values. a missing file is an error only when required.
func decodeFile(fsys fs.FS, name string, s *settings, required bool) error {
	data, err := fs.ReadFile(fsys, name)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(s); err != nil {
		return fmt.Errorf("decode %s: %w", name, errors.Join(ErrInvalid, err))
	}
	if err = dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: trailing data after the settings object: %w", name, ErrInvalid)
	}
	return nil
}

// resolve replaces every {ENV_VAR} placeholder in v with the variable's value. an unset or
// empty variable fails with the setting path and the variable name, never a value.
func resolve(path, v string, lookup func(string) (string, bool)) (string, error) {
	var missing string
	out := placeholderRe.ReplaceAllStringFunc(v, func(m string) string {
		name := m[1 : len(m)-1]
		val, _ := lookup(name)
		if val == "" && missing == "" {
			missing = name
		}
		return val
	})
	if missing != "" {
		return "", fmt.Errorf("%s: %s: %w", path, missing, ErrMissing)
	}
	return out, nil
}

// validate checks every setting and returns the decoded data key.
func (s *settings) validate() ([DataKeySize]byte, error) {
	var none [DataKeySize]byte
	if err := validateAddr(s.API.Listen); err != nil {
		return none, fmt.Errorf("Api.Listen: %w", err)
	}
	if err := validateAddr(s.Health.Listen); err != nil {
		return none, fmt.Errorf("Health.Listen: %w", err)
	}
	if s.API.Listen == s.Health.Listen {
		return none, fmt.Errorf("Health.Listen: must differ from Api.Listen: %w", ErrInvalid)
	}
	if s.API.MaxBodyBytes <= 0 {
		return none, fmt.Errorf("Api.MaxBodyBytes: must be a positive integer: %w", ErrInvalid)
	}

	for _, f := range []struct{ path, v string }{
		{"Database.Host", s.Database.Host},
		{"Database.Name", s.Database.Name},
		{"Database.User", s.Database.User},
		{"Database.Password", s.Database.Password},
	} {
		if f.v == "" {
			return none, fmt.Errorf("%s: %w", f.path, ErrMissing)
		}
	}
	if s.Database.Port < 1 || s.Database.Port > 65535 {
		return none, fmt.Errorf("Database.Port: must be 1-65535: %w", ErrInvalid)
	}
	if s.Database.SSLMode != "" && !slices.Contains(sslModes, s.Database.SSLMode) {
		return none, fmt.Errorf("Database.SSLMode: must be one of %s: %w", strings.Join(sslModes, ", "), ErrInvalid)
	}
	key, err := decodeKey(s.DataKey)
	if err != nil {
		return none, fmt.Errorf("DataKey: %w", err)
	}
	return key, nil
}

// dsn builds the postgres connection string; url.URL escapes any character of the credentials.
func (s *settings) dsn() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(s.Database.User, s.Database.Password),
		Host:   net.JoinHostPort(s.Database.Host, strconv.Itoa(s.Database.Port)),
		Path:   "/" + s.Database.Name,
	}
	if s.Database.SSLMode != "" {
		u.RawQuery = url.Values{"sslmode": {s.Database.SSLMode}}.Encode()
	}
	return u.String()
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
