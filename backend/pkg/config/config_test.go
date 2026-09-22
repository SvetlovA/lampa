package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPassword = "s3cret-pass"
	testFile     = "appsettings.Test.json"
)

// baseJSON matches the shape of the shipped appsettings.json
const baseJSON = `{
  "Api": {"Listen": ":5800", "MaxBodyBytes": 2097152},
  "Health": {"Listen": ":8081"},
  "Database": {"Host": "localhost", "Port": 5434, "Name": "lampa", "User": "lampa",
               "Password": "{LAMPA_DB_PASSWORD}", "SSLMode": "disable"},
  "DataKey": "{LAMPA_API_DATA_KEY}"
}`

var (
	testKey    = bytes.Repeat([]byte{0xAB}, DataKeySize)
	testKeyB64 = base64.StdEncoding.EncodeToString(testKey)
)

// envOf returns a lookup func backed by a map, with both placeholder variables prefilled.
func envOf(t *testing.T, overrides map[string]string) func(string) (string, bool) {
	t.Helper()
	env := map[string]string{
		"LAMPA_DB_PASSWORD":  testPassword,
		"LAMPA_API_DATA_KEY": testKeyB64,
	}
	maps.Copy(env, overrides)
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// fsOf returns baseJSON as appsettings.json plus the given files, which may replace it.
func fsOf(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{baseFile: {Data: []byte(baseJSON)}}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return fsys
}

// parseDSN parses the DSN the way pgxpool does in main.
func parseDSN(t *testing.T, dsn string) *pgconn.Config {
	t.Helper()
	pc, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	return pc
}

func TestLoad_base(t *testing.T) {
	cfg, err := Load(fsOf(nil), envOf(t, nil))
	require.NoError(t, err)

	assert.Equal(t, Test, cfg.Environment)
	assert.Equal(t, ":5800", cfg.Listen)
	assert.Equal(t, ":8081", cfg.HealthListen)
	assert.Equal(t, "postgres://lampa:"+testPassword+"@localhost:5434/lampa?sslmode=disable", cfg.DBDSN)
	assert.Equal(t, testKey, cfg.DataKey[:])
	assert.Equal(t, int64(2097152), cfg.MaxBodyBytes)
}

func TestLoad_environments(t *testing.T) {
	fsys := fsOf(map[string]string{
		testFile:                      `{"Database": {"Host": "test-db", "Port": 5432}}`,
		"appsettings.Production.json": `{"Database": {"Host": "prod-db", "Port": 6432}}`,
	})
	tests := []struct {
		name     string
		env      map[string]string
		wantEnv  string
		wantHost string
		wantPort uint16
	}{
		{name: "unset defaults to test", env: nil, wantEnv: Test, wantHost: "test-db", wantPort: 5432},
		{name: "empty defaults to test", env: map[string]string{EnvEnvironment: ""}, wantEnv: Test, wantHost: "test-db", wantPort: 5432},
		{name: "test", env: map[string]string{EnvEnvironment: Test}, wantEnv: Test, wantHost: "test-db", wantPort: 5432},
		{name: "production", env: map[string]string{EnvEnvironment: Production}, wantEnv: Production, wantHost: "prod-db",
			wantPort: 6432},
		{name: "development without its own file uses the base", env: map[string]string{EnvEnvironment: Development},
			wantEnv: Development, wantHost: "localhost", wantPort: 5434},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(fsys, envOf(t, tc.env))
			require.NoError(t, err)
			assert.Equal(t, tc.wantEnv, cfg.Environment)
			pc := parseDSN(t, cfg.DBDSN)
			assert.Equal(t, tc.wantHost, pc.Host)
			assert.Equal(t, tc.wantPort, pc.Port)
		})
	}
}

func TestLoad_layering(t *testing.T) {
	tests := []struct {
		name     string
		override string
		check    func(t *testing.T, cfg Config)
	}{
		{name: "untouched nested keys keep base values", override: `{"Database": {"Host": "other"}}`,
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, "postgres://lampa:"+testPassword+"@other:5434/lampa?sslmode=disable", cfg.DBDSN)
				assert.Equal(t, ":5800", cfg.Listen)
				assert.Equal(t, ":8081", cfg.HealthListen)
				assert.Equal(t, int64(2097152), cfg.MaxBodyBytes)
			}},
		{name: "one key of a section", override: `{"Api": {"MaxBodyBytes": 1024}}`,
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, int64(1024), cfg.MaxBodyBytes)
				assert.Equal(t, ":5800", cfg.Listen)
			}},
		{name: "empty override", override: `{}`,
			check: func(t *testing.T, cfg Config) { assert.Equal(t, ":5800", cfg.Listen) }},
		{name: "listen port 0 is accepted", override: `{"Api": {"Listen": "127.0.0.1:0"}, "Health": {"Listen": "localhost:0"}}`,
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, "127.0.0.1:0", cfg.Listen)
				assert.Equal(t, "localhost:0", cfg.HealthListen)
			}},
		{name: "literal password without placeholder", override: `{"Database": {"Password": "plain"}}`,
			check: func(t *testing.T, cfg Config) { assert.Equal(t, "plain", parseDSN(t, cfg.DBDSN).Password) }},
		{name: "substring placeholders", override: `{"Database": {"Password": "pre-{LAMPA_DB_PASSWORD}-{EXTRA_PART}"}}`,
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, "pre-"+testPassword+"-x", parseDSN(t, cfg.DBDSN).Password)
			}},
		{name: "lowercase braces are not placeholders", override: `{"Database": {"Password": "{not_a_var}"}}`,
			check: func(t *testing.T, cfg Config) { assert.Equal(t, "{not_a_var}", parseDSN(t, cfg.DBDSN).Password) }},
		{name: "no ssl mode omits the query", override: `{"Database": {"SSLMode": ""}}`,
			check: func(t *testing.T, cfg Config) {
				assert.Equal(t, "postgres://lampa:"+testPassword+"@localhost:5434/lampa", cfg.DBDSN)
			}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(fsOf(map[string]string{testFile: tc.override}), envOf(t, map[string]string{"EXTRA_PART": "x"}))
			require.NoError(t, err)
			tc.check(t, cfg)
		})
	}
}

func TestLoad_dsnEscaping(t *testing.T) {
	const password = `p@ss:w/o?r#d%20 x`
	override := `{"Database": {"Name": "lampa db", "User": "lam:pa"}}`
	cfg, err := Load(fsOf(map[string]string{testFile: override}), envOf(t, map[string]string{"LAMPA_DB_PASSWORD": password}))
	require.NoError(t, err)

	u, err := url.Parse(cfg.DBDSN)
	require.NoError(t, err)
	pass, _ := u.User.Password()
	assert.Equal(t, password, pass)
	assert.Equal(t, "lam:pa", u.User.Username())

	pc := parseDSN(t, cfg.DBDSN)
	assert.Equal(t, password, pc.Password)
	assert.Equal(t, "lam:pa", pc.User)
	assert.Equal(t, "lampa db", pc.Database)
	assert.Equal(t, "localhost", pc.Host)
	assert.Equal(t, uint16(5434), pc.Port)
}

func TestLoad_invalid(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string // replaces files of fsOf, baseFile included
		noBase   bool
		env      map[string]string
		wantErr  error
		wantMsg  string // exact error text when wantFull, else a substring
		wantFull bool
	}{
		{name: "unknown environment", env: map[string]string{EnvEnvironment: "Staging"}, wantErr: ErrInvalid,
			wantMsg: "LAMPA_ENVIRONMENT: must be one of Development, Test, Production: invalid value", wantFull: true},
		{name: "environment is case sensitive", env: map[string]string{EnvEnvironment: "production"}, wantErr: ErrInvalid,
			wantMsg: "LAMPA_ENVIRONMENT"},
		{name: "missing base file", noBase: true, wantErr: fs.ErrNotExist, wantMsg: "read appsettings.json"},
		{name: "malformed base", files: map[string]string{baseFile: `{"Api": `}, wantErr: ErrInvalid,
			wantMsg: "decode appsettings.json"},
		{name: "malformed environment file", files: map[string]string{testFile: `{"Api": [}`}, wantErr: ErrInvalid,
			wantMsg: "decode appsettings.Test.json"},
		{name: "trailing data", files: map[string]string{baseFile: baseJSON + ` {}`}, wantErr: ErrInvalid,
			wantMsg: "decode appsettings.json: trailing data after the settings object: invalid value", wantFull: true},
		{name: "trailing garbage", files: map[string]string{testFile: `{} x`}, wantErr: ErrInvalid,
			wantMsg: "decode appsettings.Test.json: trailing data"},
		{name: "unknown key", files: map[string]string{testFile: `{"Api": {"Port": 1}}`}, wantErr: ErrInvalid,
			wantMsg: `unknown field "Port"`},
		{name: "unknown section", files: map[string]string{testFile: `{"Kestrel": {}}`}, wantErr: ErrInvalid,
			wantMsg: `unknown field "Kestrel"`},
		{name: "wrong type", files: map[string]string{testFile: `{"Database": {"Port": "5432"}}`}, wantErr: ErrInvalid,
			wantMsg: "decode appsettings.Test.json"},
		{name: "unset password variable", env: map[string]string{"LAMPA_DB_PASSWORD": ""}, wantErr: ErrMissing,
			wantMsg: "Database.Password: LAMPA_DB_PASSWORD: required value is not set", wantFull: true},
		{name: "unset password reported before key", env: map[string]string{"LAMPA_DB_PASSWORD": "", "LAMPA_API_DATA_KEY": ""},
			wantErr: ErrMissing, wantMsg: "Database.Password: LAMPA_DB_PASSWORD: required value is not set", wantFull: true},
		{name: "empty key variable", env: map[string]string{"LAMPA_API_DATA_KEY": ""}, wantErr: ErrMissing,
			wantMsg: "DataKey: LAMPA_API_DATA_KEY: required value is not set", wantFull: true},
		{name: "unset variable inside a substring",
			files:   map[string]string{testFile: `{"Database": {"Password": "a{LAMPA_DB_PASSWORD}{NOT_SET}"}}`},
			wantErr: ErrMissing, wantMsg: "Database.Password: NOT_SET: required value is not set", wantFull: true},
		{name: "listen without port", files: map[string]string{testFile: `{"Api": {"Listen": "localhost"}}`}, wantErr: ErrInvalid,
			wantMsg: "Api.Listen: must be host:port: invalid value", wantFull: true},
		{name: "listen named port", files: map[string]string{testFile: `{"Api": {"Listen": ":http"}}`}, wantErr: ErrInvalid,
			wantMsg: "Api.Listen: port must be 0-65535"},
		{name: "health listen out of range", files: map[string]string{testFile: `{"Health": {"Listen": ":70000"}}`},
			wantErr: ErrInvalid, wantMsg: "Health.Listen: port must be 0-65535"},
		{name: "same listen", files: map[string]string{testFile: `{"Health": {"Listen": ":5800"}}`}, wantErr: ErrInvalid,
			wantMsg: "Health.Listen: must differ from Api.Listen: invalid value", wantFull: true},
		{name: "db port 0", files: map[string]string{testFile: `{"Database": {"Port": 0}}`}, wantErr: ErrInvalid,
			wantMsg: "Database.Port: must be 1-65535: invalid value", wantFull: true},
		{name: "db port too high", files: map[string]string{testFile: `{"Database": {"Port": 65536}}`}, wantErr: ErrInvalid,
			wantMsg: "Database.Port: must be 1-65535"},
		{name: "zero body limit", files: map[string]string{testFile: `{"Api": {"MaxBodyBytes": 0}}`}, wantErr: ErrInvalid,
			wantMsg: "Api.MaxBodyBytes: must be a positive integer: invalid value", wantFull: true},
		{name: "negative body limit", files: map[string]string{testFile: `{"Api": {"MaxBodyBytes": -5}}`}, wantErr: ErrInvalid,
			wantMsg: "Api.MaxBodyBytes"},
		{name: "missing db host", files: map[string]string{testFile: `{"Database": {"Host": ""}}`}, wantErr: ErrMissing,
			wantMsg: "Database.Host: required value is not set", wantFull: true},
		{name: "missing db name", files: map[string]string{testFile: `{"Database": {"Name": ""}}`}, wantErr: ErrMissing,
			wantMsg: "Database.Name: required value is not set", wantFull: true},
		{name: "missing db user", files: map[string]string{testFile: `{"Database": {"User": ""}}`}, wantErr: ErrMissing,
			wantMsg: "Database.User: required value is not set", wantFull: true},
		{name: "missing db password", files: map[string]string{testFile: `{"Database": {"Password": ""}}`}, wantErr: ErrMissing,
			wantMsg: "Database.Password: required value is not set", wantFull: true},
		{name: "bad ssl mode", files: map[string]string{testFile: `{"Database": {"SSLMode": "on"}}`}, wantErr: ErrInvalid,
			wantMsg: "Database.SSLMode: must be one of disable, allow, prefer, require, verify-ca, verify-full"},
		{name: "missing data key", files: map[string]string{testFile: `{"DataKey": ""}`}, wantErr: ErrMissing,
			wantMsg: "DataKey: required value is not set", wantFull: true},
		{name: "bad base64 key", env: map[string]string{"LAMPA_API_DATA_KEY": "not*base64!"}, wantErr: ErrInvalid,
			wantMsg: "DataKey: decode base64"},
		{name: "short key", env: map[string]string{"LAMPA_API_DATA_KEY": base64.StdEncoding.EncodeToString(make([]byte, 16))},
			wantErr: ErrInvalid, wantMsg: "DataKey: must decode to 32 bytes, got 16: invalid value", wantFull: true},
		{name: "long key", env: map[string]string{"LAMPA_API_DATA_KEY": base64.StdEncoding.EncodeToString(make([]byte, 33))},
			wantErr: ErrInvalid, wantMsg: "DataKey: must decode to 32 bytes, got 33"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fsOf(tc.files)
			if tc.noBase {
				delete(fsys, baseFile)
			}
			_, err := Load(fsys, envOf(t, tc.env))
			require.ErrorIs(t, err, tc.wantErr)
			if tc.wantFull {
				require.EqualError(t, err, tc.wantMsg)
			} else {
				assert.Contains(t, err.Error(), tc.wantMsg)
			}
			assert.NotContains(t, err.Error(), testPassword, "error must not echo the password")
			for name, v := range tc.env {
				if v != "" && name != EnvEnvironment {
					assert.NotContains(t, err.Error(), v, "error must not echo the value of %s", name)
				}
			}
		})
	}
}

func TestMustSub(t *testing.T) {
	fsys := fstest.MapFS{"dir/a.json": {Data: []byte("{}")}}

	_, err := fs.Stat(mustSub(fsys, "dir"), "a.json")
	require.NoError(t, err)
	assert.Panics(t, func() { mustSub(fsys, "../dir") })
}

func TestLoad_embeddedDefaults(t *testing.T) {
	names, err := fs.Glob(Defaults, "appsettings*.json")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"appsettings.json", "appsettings.Test.json", "appsettings.Production.json"}, names)

	tests := []struct {
		env      string
		wantHost string
		wantPort uint16
	}{
		{env: Development, wantHost: "localhost", wantPort: 5434},
		{env: Test, wantHost: "lampa-db", wantPort: 5432},
		{env: Production, wantHost: "lampa-db", wantPort: 5432},
	}
	for _, tc := range tests {
		t.Run(tc.env, func(t *testing.T) {
			cfg, err := Load(Defaults, envOf(t, map[string]string{EnvEnvironment: tc.env}))
			require.NoError(t, err)

			assert.Equal(t, tc.env, cfg.Environment)
			assert.Equal(t, ":5800", cfg.Listen)
			assert.Equal(t, ":8081", cfg.HealthListen)
			assert.Equal(t, int64(2097152), cfg.MaxBodyBytes)
			assert.Equal(t, testKey, cfg.DataKey[:])

			pc := parseDSN(t, cfg.DBDSN)
			assert.Equal(t, tc.wantHost, pc.Host)
			assert.Equal(t, tc.wantPort, pc.Port)
			assert.Equal(t, "lampa", pc.Database)
			assert.Equal(t, "lampa", pc.User)
			assert.Equal(t, testPassword, pc.Password)
			assert.Equal(t, "sslmode=disable", mustURL(t, cfg.DBDSN).RawQuery)
		})
	}

	t.Run("without variables", func(t *testing.T) {
		_, err := Load(Defaults, func(string) (string, bool) { return "", false })
		require.EqualError(t, err, "Database.Password: LAMPA_DB_PASSWORD: required value is not set")
	})
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	require.NoError(t, err)
	return u
}

func TestConfig_String(t *testing.T) {
	cfg, err := Load(fsOf(nil), envOf(t, nil))
	require.NoError(t, err)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		t.Run(verb, func(t *testing.T) {
			out := fmt.Sprintf(verb, cfg)
			assert.NotContains(t, out, testPassword)
			assert.NotContains(t, out, "localhost")
			assert.NotContains(t, out, testKeyB64)
			assert.NotContains(t, out, "171", "raw key bytes must not be printed") // 0xAB
			assert.Contains(t, out, "DBDSN:[redacted] DataKey:[redacted]")
			assert.Contains(t, out, "Environment:Test")
			assert.Contains(t, out, ":5800")
		})
	}

	t.Run("pointer", func(t *testing.T) {
		out := fmt.Sprintf("%+v", &cfg)
		assert.NotContains(t, out, testPassword)
		assert.Contains(t, out, "[redacted]")
	})

	t.Run("go string prefix", func(t *testing.T) {
		assert.Equal(t, "config.Config"+cfg.String(), fmt.Sprintf("%#v", cfg))
	})

	t.Run("unset secrets", func(t *testing.T) {
		assert.Contains(t, Config{}.String(), "DBDSN:[unset] DataKey:[unset]")
	})
}
