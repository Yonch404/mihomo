package singbox

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseConfig(t *testing.T) {
	cfg, err := ParseConfig(RawConfig{Raw: `{"log":{"level":"info"}}`})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.JSONEq(t, `{"log":{"level":"info"}}`, string(cfg.RawJSON))
	require.NotEmpty(t, cfg.Hash)
}

func TestParseConfigEmpty(t *testing.T) {
	cfg, err := ParseConfig(RawConfig{})
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestParseConfigRejectInvalidJSON(t *testing.T) {
	_, err := ParseConfig(RawConfig{Raw: `{"log":`})
	require.Error(t, err)
}

func TestParseConfigRejectNonObject(t *testing.T) {
	_, err := ParseConfig(RawConfig{Raw: `[]`})
	require.Error(t, err)
}

func TestRawConfigUnmarshalYAMLScalar(t *testing.T) {
	var out struct {
		SingBox RawConfig `yaml:"sing-box"`
	}
	err := yaml.Unmarshal([]byte("sing-box: '{\"log\":{\"level\":\"info\"}}'"), &out)
	require.NoError(t, err)
	require.Equal(t, `{"log":{"level":"info"}}`, out.SingBox.Raw)
}

func TestRawConfigUnmarshalYAMLObject(t *testing.T) {
	var out struct {
		SingBox RawConfig `yaml:"sing-box"`
	}
	err := yaml.Unmarshal([]byte("sing-box:\n  log:\n    level: info\n"), &out)
	require.NoError(t, err)

	cfg, err := ParseConfig(out.SingBox)
	require.NoError(t, err)
	require.JSONEq(t, `{"log":{"level":"info"}}`, string(cfg.RawJSON))
}
