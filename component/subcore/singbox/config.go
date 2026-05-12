package singbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RawConfig keeps the sing-box configuration exactly as the user supplied it,
// while still allowing YAML object syntax for convenience.
type RawConfig struct {
	Raw string
}

func (c RawConfig) Enabled() bool {
	return strings.TrimSpace(c.Raw) != ""
}

func (c *RawConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Tag == "!!null" {
		c.Raw = ""
		return nil
	}

	switch value.Kind {
	case yaml.ScalarNode:
		c.Raw = value.Value
		return nil
	case yaml.MappingNode:
		var decoded any
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		normalized, err := normalizeYAMLValue(decoded)
		if err != nil {
			return err
		}
		buf, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		c.Raw = string(buf)
		return nil
	default:
		return fmt.Errorf("sing-box config must be a JSON string or object")
	}
}

func (c RawConfig) MarshalYAML() (any, error) {
	if !c.Enabled() {
		return nil, nil
	}
	return c.Raw, nil
}

func (c *RawConfig) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		c.Raw = ""
		return nil
	}

	var raw string
	if err := json.Unmarshal(data, &raw); err == nil {
		c.Raw = raw
		return nil
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return fmt.Errorf("sing-box config must be a JSON string or object: %w", err)
	}
	c.Raw = compact.String()
	return nil
}

func (c RawConfig) MarshalJSON() ([]byte, error) {
	if !c.Enabled() {
		return []byte("null"), nil
	}
	return json.Marshal(c.Raw)
}

type Config struct {
	RawJSON []byte
	Hash    string
}

func ParseConfig(raw RawConfig) (*Config, error) {
	data := []byte(strings.TrimSpace(raw.Raw))
	if len(data) == 0 {
		return nil, nil
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, fmt.Errorf("sing-box config invalid JSON: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(compact.Bytes()))
	decoder.UseNumber()

	var top any
	if err := decoder.Decode(&top); err != nil {
		return nil, fmt.Errorf("sing-box config invalid JSON: %w", err)
	}
	if _, ok := top.(map[string]any); !ok {
		return nil, fmt.Errorf("sing-box config must be a JSON object")
	}

	sum := sha256.Sum256(compact.Bytes())
	return &Config{
		RawJSON: append([]byte(nil), compact.Bytes()...),
		Hash:    hex.EncodeToString(sum[:]),
	}, nil
}

func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	return &Config{
		RawJSON: append([]byte(nil), c.RawJSON...),
		Hash:    c.Hash,
	}
}

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			next, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			normalized[key] = next
		}
		return normalized, nil
	case map[any]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("sing-box config contains a non-string key: %v", key)
			}
			next, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			normalized[keyString] = next
		}
		return normalized, nil
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			next, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			normalized[index] = next
		}
		return normalized, nil
	default:
		return value, nil
	}
}
