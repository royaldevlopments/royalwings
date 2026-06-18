package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Token     string        `yaml:"token"`
	Panel     PanelConfig   `yaml:"panel"`
	System    SystemConfig  `yaml:"system"`
	Docker    DockerConfig  `yaml:"docker"`
	SFTP      SFTPConfig    `yaml:"sftp"`
	Activity  ActivityConfig `yaml:"activity"`
	Backups   BackupsConfig `yaml:"backups"`
	Debug     bool          `yaml:"debug"`
	Data      string        `yaml:"data"`
}

type PanelConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type SystemConfig struct {
	HTTP     string `yaml:"http"`
	Host     string `yaml:"host"`
	Timezone string `yaml:"timezone"`
	TmpDir   string `yaml:"tmp_dir"`
}

type DockerConfig struct {
	Socket    string `yaml:"socket"`
	Network   string `yaml:"network"`
	Domain    string `yaml:"domain"`
}

type SFTPConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type ActivityConfig struct {
	LogFile string `yaml:"log_file"`
}

type BackupsConfig struct {
	Directory string `yaml:"directory"`
	WriteLimit int64 `yaml:"write_limit"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{
		System: SystemConfig{
			HTTP:     "0.0.0.0:8080",
			Host:     "0.0.0.0",
			Timezone: "UTC",
			TmpDir:   "/tmp/royalwings",
		},
		Docker: DockerConfig{
			Socket:  "/var/run/docker.sock",
			Network: "royalwings",
		},
		SFTP: SFTPConfig{
			Address: "0.0.0.0",
			Port:    2022,
		},
		Backups: BackupsConfig{
			Directory:  "/var/lib/royalwings/backups",
			WriteLimit: 5 * 1024 * 1024 * 1024,
		},
		Activity: ActivityConfig{
			LogFile: "/var/lib/royalwings/activity.log",
		},
		Data: "/var/lib/royalwings",
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	cfg.resolveEnv()

	return cfg, nil
}

func (c *Config) resolveEnv() {
	if c.Token == "" {
		c.Token = os.Getenv("ROYALWINGS_TOKEN")
	}
	if c.Panel.Token == "" {
		c.Panel.Token = os.Getenv("ROYALWINGS_PANEL_TOKEN")
	}
	if c.Panel.URL == "" {
		c.Panel.URL = os.Getenv("ROYALWINGS_PANEL_URL")
	}
}
