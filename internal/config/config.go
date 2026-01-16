/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package config

import (
	"fmt"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	Database DatabaseConfig `mapstructure:"database"`
	GRPC     GRPCConfig     `mapstructure:"grpc"`
	SSH      SSHConfig      `mapstructure:"ssh"`
	K8s      K8sConfig      `mapstructure:"k8s"`
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	Host            string `mapstructure:"host"`
	Port            int    `mapstructure:"port"`
	Name            string `mapstructure:"name"`
	User            string `mapstructure:"user"`
	Password        string `mapstructure:"password"`
	SSLMode         string `mapstructure:"ssl_mode"`
	MaxOpenConns    int    `mapstructure:"max_open_conns"`
	MaxIdleConns    int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetime int    `mapstructure:"conn_max_lifetime"` // in seconds
}

// GRPCConfig holds gRPC server settings
type GRPCConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

// SSHConfig holds SSH connection pool settings
type SSHConfig struct {
	MaxIdleConnections    int `mapstructure:"max_idle_connections"`
	MaxConnectionDuration int `mapstructure:"max_connection_duration"` // in seconds
	ConnectionTimeout     int `mapstructure:"connection_timeout"`      // in seconds
	MaxRetries            int `mapstructure:"max_retries"`
	RetryInterval         int `mapstructure:"retry_interval"` // in milliseconds
}

// K8sConfig holds Kubernetes configuration
type K8sConfig struct {
	Kubeconfig string `mapstructure:"kubeconfig"`
	Namespace  string `mapstructure:"namespace"`
}

// DSN returns the PostgreSQL connection string
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		d.Host, d.Port, d.Name, d.User, d.Password, d.SSLMode,
	)
}

// ConnMaxLifetimeDuration returns the connection max lifetime as duration
func (d DatabaseConfig) ConnMaxLifetimeDuration() time.Duration {
	return time.Duration(d.ConnMaxLifetime) * time.Second
}

// ConnectionTimeoutDuration returns the SSH connection timeout as duration
func (s SSHConfig) ConnectionTimeoutDuration() time.Duration {
	return time.Duration(s.ConnectionTimeout) * time.Second
}

// RetryIntervalDuration returns the retry interval as duration
func (s SSHConfig) RetryIntervalDuration() time.Duration {
	return time.Duration(s.RetryInterval) * time.Millisecond
}

// Load reads configuration from the given file path
func Load(configPath string) (*Config, error) {
	v := viper.New()

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/ksa")
		v.AddConfigPath("$HOME/.ksa")
	}

	// Allow environment variable overrides
	v.SetEnvPrefix("KSA")
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set defaults
	cfg.setDefaults()

	return &cfg, nil
}

func (c *Config) setDefaults() {
	if c.Database.SSLMode == "" {
		c.Database.SSLMode = "disable"
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = 25
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = 5
	}
	if c.Database.ConnMaxLifetime == 0 {
		c.Database.ConnMaxLifetime = 300
	}
	if c.GRPC.Port == 0 {
		c.GRPC.Port = 443
	}
	if c.SSH.MaxIdleConnections == 0 {
		c.SSH.MaxIdleConnections = 10
	}
	if c.SSH.MaxConnectionDuration == 0 {
		c.SSH.MaxConnectionDuration = 300
	}
	if c.SSH.ConnectionTimeout == 0 {
		c.SSH.ConnectionTimeout = 30
	}
	if c.SSH.MaxRetries == 0 {
		c.SSH.MaxRetries = 3
	}
	if c.SSH.RetryInterval == 0 {
		c.SSH.RetryInterval = 1000
	}
	if c.K8s.Namespace == "" {
		c.K8s.Namespace = "default"
	}
}

// LoadKubeconfig loads a Kubernetes config from a file
func LoadKubeconfig(path string) error {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("kubeconfig file not found: %s", path)
	}
	if err != nil {
		return fmt.Errorf("failed to check kubeconfig: %w", err)
	}
	return nil
}

// LoadDatabaseConfig loads database configuration from environment variables
// Used primarily for testing or when config file is not available
func LoadDatabaseConfig() (*DatabaseConfig, error) {
	host := os.Getenv("KSA_DB_HOST")
	if host == "" {
		host = "localhost"
	}

	port := 5432
	if p := os.Getenv("KSA_DB_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}

	name := os.Getenv("KSA_DB_NAME")
	if name == "" {
		return nil, fmt.Errorf("database name is required")
	}

	user := os.Getenv("KSA_DB_USER")
	if user == "" {
		user = "ksa"
	}

	password := os.Getenv("KSA_DB_PASSWORD")
	sslmode := os.Getenv("KSA_DB_SSLMODE")
	if sslmode == "" {
		sslmode = "disable"
	}

	return &DatabaseConfig{
		Host:     host,
		Port:     port,
		Name:     name,
		User:     user,
		Password: password,
		SSLMode:  sslmode,
	}, nil
}

// RegisterMetrics registers database config metrics
func (c *DatabaseConfig) RegisterMetrics(registry prometheus.Collector) {
	// Metrics can be registered here if needed
	_ = registry
}
