package migrations

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFS(t *testing.T) {
	names, err := fs.Glob(FS, "*.sql")
	require.NoError(t, err)
	assert.Equal(t, []string{"00001_create_lampa_user_data.sql"}, names)

	for _, name := range names {
		content, err := fs.ReadFile(FS, name)
		require.NoError(t, err)
		assert.Contains(t, string(content), "-- +goose Up", name)
		assert.Contains(t, string(content), "-- +goose Down", name)
	}
}
