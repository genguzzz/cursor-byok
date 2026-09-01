// Package config loads and validates the tab-server configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrMissingToken is returned when the upstream token is empty.
var ErrMissingToken = errors.New("token 不能为空")

// Config is the tab-server configuration.
type Config struct {
	Token string `yaml:"token"`

	Upstream UpstreamConfig `yaml:"upstream"`
	Tab      TabConfig      `yaml:"tab"`
	Server   ServerConfig   `yaml:"server"`
}

// UpstreamConfig describes the CodeBuddy Chat Completions endpoint and its
// CLI-identifying headers. The gateway gates behaviour on these values, so
// they are not cosmetic.
type UpstreamConfig struct {
	BaseURL    string            `yaml:"base_url"`
	Endpoint   string            `yaml:"endpoint"`
	Model      string            `yaml:"model"`
	Headers    map[string]string `yaml:"headers"`
	TimeoutSec int               `yaml:"timeout_sec"`
}

// TabConfig tunes Tab behaviour.
type TabConfig struct {
	MaxInputChars      int  `yaml:"max_input_chars"`
	MaxOutputTokens    int  `yaml:"max_output_tokens"`
	EnableNextEdit     bool `yaml:"enable_next_edit"`
	EnableGitCommitMsg bool `yaml:"enable_git_commit_message"`
}

// ServerConfig tunes the HTTP listener.
type ServerConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	LogEnabled bool   `yaml:"log"`
}

// Defaults returns the configuration used when a key is absent from the file.
func Defaults() Config {
	return Config{
		Upstream: UpstreamConfig{
			BaseURL:    "https://copilot.tencent.com/v2",
			Endpoint:   "/chat/completions",
			Model:      "deepseek-v4-flash-ioa",
			TimeoutSec: 30,
			Headers:    map[string]string{},
		},
		Tab: TabConfig{
			MaxInputChars:      12000,
			MaxOutputTokens:    256,
			EnableNextEdit:     true,
			EnableGitCommitMsg: true,
		},
		Server: ServerConfig{
			ListenAddr: ":8041",
			LogEnabled: true,
		},
	}
}

// Load reads path and merges it onto Defaults.
func Load(path string) (Config, error) {
	config := Defaults()
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return Config{}, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate rejects a configuration that cannot serve Tab requests.
func (c *Config) Validate() error {
	c.Upstream.BaseURL = strings.TrimSpace(c.Upstream.BaseURL)
	c.Upstream.Endpoint = strings.TrimSpace(c.Upstream.Endpoint)
	c.Upstream.Model = strings.TrimSpace(c.Upstream.Model)
	c.Server.ListenAddr = strings.TrimSpace(c.Server.ListenAddr)
	if c.Upstream.BaseURL == "" {
		return errors.New("upstream.base_url 不能为空")
	}
	if c.Upstream.Endpoint == "" {
		return errors.New("upstream.endpoint 不能为空")
	}
	if c.Upstream.Model == "" {
		return errors.New("upstream.model 不能为空")
	}
	if c.Server.ListenAddr == "" {
		return errors.New("server.listen_addr 不能为空")
	}
	if c.Tab.MaxInputChars <= 0 {
		return errors.New("tab.max_input_chars 必须大于零")
	}
	if c.Tab.MaxOutputTokens <= 0 {
		return errors.New("tab.max_output_tokens 必须大于零")
	}
	if strings.TrimSpace(c.Token) == "" {
		return ErrMissingToken
	}
	c.Token = strings.TrimSpace(c.Token)
	c.Upstream.BaseURL = strings.TrimRight(c.Upstream.BaseURL, "/")
	if !strings.HasPrefix(c.Upstream.Endpoint, "/") {
		c.Upstream.Endpoint = "/" + c.Upstream.Endpoint
	}
	return nil
}
