package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDSN = "postgres://lampa:s3cret-pass@lampa-db:5432/lampa?sslmode=disable"

var testKey = bytes.Repeat([]byte{0xAB}, DataKeySize)

// envOf returns a lookup func backed by a map, with the required variables prefilled.
func envOf(t *testing.T, overrides map[string]string) func(string) (string, bool) {
	t.Helper()
	env := map[string]string{
		envDBDSN:   testDSN,
		envDataKey: base64.StdEncoding.EncodeToString(testKey),
	}
	maps.Copy(env, overrides)
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

func TestLoad_defaults(t *testing.T) {
	cfg, err := Load(envOf(t, nil))
	require.NoError(t, err)

	assert.Equal(t, ":8080", cfg.Listen)
	assert.Equal(t, ":8081", cfg.HealthListen)
	assert.Equal(t, testDSN, cfg.DBDSN)
	assert.Equal(t, testKey, cfg.DataKey[:])
	assert.Equal(t, int64(2097152), cfg.MaxBodyBytes)
}

func TestLoad_overrides(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, cfg Config)
	}{
		{name: "listen", env: map[string]string{envListen: "127.0.0.1:9000"},
			check: func(t *testing.T, cfg Config) { assert.Equal(t, "127.0.0.1:9000", cfg.Listen) }},
		{name: "health listen", env: map[string]string{envHealthListen: "0.0.0.0:9001"},
			check: func(t *testing.T, cfg Config) { assert.Equal(t, "0.0.0.0:9001", cfg.HealthListen) }},
		{name: "dsn", env: map[string]string{envDBDSN: "postgres://other"},
			check: func(t *testing.T, cfg Config) { assert.Equal(t, "postgres://other", cfg.DBDSN) }},
		{name: "data key", env: map[string]string{envDataKey: base64.StdEncoding.EncodeToString(make([]byte, DataKeySize))},
			check: func(t *testing.T, cfg Config) { assert.Equal(t, [DataKeySize]byte{}, cfg.DataKey) }},
		{name: "max body bytes", env: map[string]string{envMaxBodyBytes: "1024"},
			check: func(t *testing.T, cfg Config) { assert.Equal(t, int64(1024), cfg.MaxBodyBytes) }},
		{name: "empty optional falls back to default", env: map[string]string{envListen: "", envMaxBodyBytes: ""},
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, ":8080", cfg.Listen)
				assert.Equal(t, int64(2097152), cfg.MaxBodyBytes)
			}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(envOf(t, tc.env))
			require.NoError(t, err)
			tc.check(t, cfg)
		})
	}
}

func TestLoad_invalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
		wantErr error
	}{
		{name: "missing dsn", env: map[string]string{envDBDSN: ""}, wantVar: envDBDSN, wantErr: ErrMissing},
		{name: "missing key", env: map[string]string{envDataKey: ""}, wantVar: envDataKey, wantErr: ErrMissing},
		{name: "bad base64 key", env: map[string]string{envDataKey: "not*base64!"}, wantVar: envDataKey, wantErr: ErrInvalid},
		{name: "short key", env: map[string]string{envDataKey: base64.StdEncoding.EncodeToString(make([]byte, 16))},
			wantVar: envDataKey, wantErr: ErrInvalid},
		{name: "long key", env: map[string]string{envDataKey: base64.StdEncoding.EncodeToString(make([]byte, 33))},
			wantVar: envDataKey, wantErr: ErrInvalid},
		{name: "zero body limit", env: map[string]string{envMaxBodyBytes: "0"}, wantVar: envMaxBodyBytes, wantErr: ErrInvalid},
		{name: "negative body limit", env: map[string]string{envMaxBodyBytes: "-5"}, wantVar: envMaxBodyBytes, wantErr: ErrInvalid},
		{name: "non-numeric body limit", env: map[string]string{envMaxBodyBytes: "2MB"}, wantVar: envMaxBodyBytes, wantErr: ErrInvalid},
		{name: "listen without port", env: map[string]string{envListen: "localhost"}, wantVar: envListen, wantErr: ErrInvalid},
		{name: "listen bad port", env: map[string]string{envListen: ":http"}, wantVar: envListen, wantErr: ErrInvalid},
		{name: "health listen port out of range", env: map[string]string{envHealthListen: ":70000"},
			wantVar: envHealthListen, wantErr: ErrInvalid},
		{name: "equal listen addresses", env: map[string]string{envListen: ":9000", envHealthListen: ":9000"},
			wantVar: envHealthListen, wantErr: ErrInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envOf(t, tc.env))
			require.Error(t, err)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Contains(t, err.Error(), tc.wantVar)
			assert.NotContains(t, err.Error(), "s3cret-pass")
			if v := tc.env[tc.wantVar]; v != "" {
				assert.NotContains(t, err.Error(), v, "error must not echo the value")
			}
		})
	}
}

func TestConfig_String(t *testing.T) {
	cfg, err := Load(envOf(t, nil))
	require.NoError(t, err)
	keyB64 := base64.StdEncoding.EncodeToString(testKey)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		t.Run(verb, func(t *testing.T) {
			out := fmt.Sprintf(verb, cfg)
			assert.NotContains(t, out, "s3cret-pass")
			assert.NotContains(t, out, "lampa-db")
			assert.NotContains(t, out, keyB64)
			assert.NotContains(t, out, "171", "raw key bytes must not be printed") // 0xAB
			assert.Contains(t, out, "[redacted]")
			assert.Contains(t, out, ":8080")
		})
	}

	t.Run("pointer", func(t *testing.T) {
		out := fmt.Sprintf("%+v", &cfg)
		assert.NotContains(t, out, "s3cret-pass")
		assert.Contains(t, out, "[redacted]")
	})

	t.Run("unset secrets", func(t *testing.T) {
		assert.Contains(t, Config{}.String(), "DBDSN:[unset] DataKey:[unset]")
	})
}
