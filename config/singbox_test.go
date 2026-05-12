package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalRawConfigSingBox(t *testing.T) {
	raw, err := UnmarshalRawConfig([]byte("mixed-port: 7890\nsing-box: '{\"log\":{\"level\":\"info\"}}'\n"))
	require.NoError(t, err)
	require.True(t, raw.SingBox.Enabled())
	require.JSONEq(t, `{"log":{"level":"info"}}`, raw.SingBox.Raw)
}
