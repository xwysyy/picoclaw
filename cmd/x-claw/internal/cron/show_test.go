package cron

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewShowSubcommand(t *testing.T) {
	fn := func() string { return "" }
	cmd := newShowCommand(fn)

	require.NotNil(t, cmd)
	assert.Equal(t, "Show a scheduled job (including last output and run history)", cmd.Short)
	assert.Equal(t, "show <job_id>", cmd.Use)

	flag := cmd.Flags().Lookup("json")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}
