package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noEnv(string) (string, bool) { return "", false }

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		wantOut string
	}{
		{name: "version flag", args: []string{"--version"}, wantOut: "lampa-api "},
		{name: "help flag", args: []string{"--help"}, wantOut: "-version"},
		{name: "unknown flag", args: []string{"--nope"}, wantErr: "parse arguments"},
		{name: "positional argument", args: []string{"extra"}, wantErr: `unexpected argument "extra"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), tc.args, noEnv, &out)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out.String(), tc.wantOut)
		})
	}
}

func TestRun_version(t *testing.T) {
	orig := revision
	t.Cleanup(func() { revision = orig })
	revision = "test-rev"

	var out bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"--version"}, noEnv, &out))
	assert.Equal(t, "lampa-api test-rev\n", out.String())
}

func TestRun_stopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, noEnv, &bytes.Buffer{}) }()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after context cancel")
	}
}

func TestResolveVersion(t *testing.T) {
	orig := revision
	t.Cleanup(func() { revision = orig })

	revision = "v1.2.3"
	assert.Equal(t, "v1.2.3", resolveVersion())

	revision = "unknown"
	assert.NotEmpty(t, resolveVersion())
}
