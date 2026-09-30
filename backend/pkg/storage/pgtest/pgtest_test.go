package pgtest

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDB(t *testing.T) {
	p := DB(t)
	require.NotNil(t, p)
	assert.Same(t, p, DB(t), "container and pool are shared by the whole test binary")

	var tables int
	err := p.QueryRow(t.Context(), `select count(*) from pg_tables where tablename = 'lampa_user_data'`).Scan(&tables)
	require.NoError(t, err)
	assert.Equal(t, 1, tables, "migrations are applied")

	var version string
	require.NoError(t, p.QueryRow(t.Context(), `show server_version`).Scan(&version))
	assert.Regexp(t, `^`+regexp.QuoteMeta(strings.TrimPrefix(Image, "postgres:"))+`\b`, version)
}
