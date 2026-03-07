package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

type Config struct {
	AuthToken string `mapstructure:"auth_token" yaml:"auth_token,omitempty"`
	ServerURL string `mapstructure:"server_url" yaml:"server_url,omitempty"`
	LogLevel  string `mapstructure:"log_level" yaml:"log_level,omitempty"`
}

var configFilePath string

// ConfigPath returns the active config file path.
func ConfigPath() string {
	return configFilePath
}

// SetConfigPath overrides the config file path (useful for testing).
func SetConfigPath(path string) {
	configFilePath = path
}

// Load reads configuration from a YAML file and environment variables.
// If configPath is empty, it looks in ~/.config/justtunnel/config.yaml.
func Load(configPath string) (*Config, error) {
	viperCfg := viper.New()
	viperCfg.SetDefault("server_url", "wss://api.justtunnel.dev/ws")
	viperCfg.SetDefault("log_level", "info")
	viperCfg.SetDefault("auth_token", "")

	viperCfg.SetConfigType("yaml")
	if configPath != "" {
		viperCfg.SetConfigFile(configPath)
		configFilePath = configPath
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			dir := filepath.Join(home, ".config", "justtunnel")
			viperCfg.AddConfigPath(dir)
			viperCfg.SetConfigName("config")
			configFilePath = filepath.Join(dir, "config.yaml")
		}
	}

	viperCfg.SetEnvPrefix("JUSTTUNNEL")
	viperCfg.AutomaticEnv()

	if err := viperCfg.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && configPath != "" {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := viperCfg.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}

// Save writes the config to the YAML file at ConfigPath().
// Creates the config directory if missing (mode 0700). File mode 0600.
func Save(cfg *Config) error {
	path := configFilePath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		path = filepath.Join(home, ".config", "justtunnel", "config.yaml")
		configFilePath = path
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

// DeleteAuthToken removes the auth_token from the config file and re-saves.
func DeleteAuthToken() error {
	path := configFilePath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		path = filepath.Join(home, ".config", "justtunnel", "config.yaml")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	cfg, err := Load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfg.AuthToken = ""
	return Save(cfg)
}
