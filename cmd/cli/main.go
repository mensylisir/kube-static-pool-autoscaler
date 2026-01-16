/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"kube-static-pool-autoscaler/internal/cli/commands"
	"kube-static-pool-autoscaler/internal/config"
	"kube-static-pool-autoscaler/internal/db"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})

	logger.Infof("Starting Kube Static Pool Autoscaler CLI v%s (build: %s)", version, buildTime)

	// Check if just showing version or help (no database needed)
	if len(os.Args) >= 2 {
		helpCommands := map[string]bool{
			"version": true, "--help": true, "-h": true,
		}
		subcommands := map[string]bool{
			"machine": true, "nodepool": true, "discover": true,
		}

		if helpCommands[os.Args[1]] || (len(os.Args) >= 3 && helpCommands[os.Args[2]]) ||
			(len(os.Args) >= 3 && subcommands[os.Args[1]] && helpCommands[os.Args[2]]) {
			// Let cobra handle these commands without database
			cfg := &config.Config{
				Database: config.DatabaseConfig{},
				GRPC:     config.GRPCConfig{},
				SSH:      config.SSHConfig{},
			}
			rootCmd := commands.NewRootCommand(cfg, nil, logger.WithField("component", "cli"))
			rootCmd.SetArgs(os.Args[1:])
			if err := rootCmd.ExecuteContext(context.Background()); err != nil {
				logger.Fatalf("Command failed: %v", err)
			}
			return
		}
	}

	configPath := "config.yaml"
	if path := os.Getenv("KSA_CONFIG"); path != "" {
		configPath = path
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	registry := prometheus.NewRegistry()

	var database *db.DB
	database, err = db.New(cfg.Database, registry)
	if err != nil {
		logger.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := commands.NewRootCommand(cfg, database, logger.WithField("component", "cli"))
	if err := cmd.ExecuteContext(ctx); err != nil {
		logger.Fatalf("Command failed: %v", err)
	}

	fmt.Println()
	_ = logger
}
