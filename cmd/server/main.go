/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kube-static-pool-autoscaler/internal/config"
	"kube-static-pool-autoscaler/internal/db"
	"kube-static-pool-autoscaler/internal/grpc"
	"kube-static-pool-autoscaler/internal/ssh"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "/etc/ksa/config.yaml", "Path to configuration file")
	metricsAddr := flag.String("metrics", ":9090", "Metrics server address")
	flag.Parse()

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})

	logger.Infof("Starting Kube Static Pool Autoscaler Server v%s (build: %s)", version, buildTime)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	logger.Infof("Configuration loaded from %s", *configPath)

	registry := prometheus.NewRegistry()

	database, err := db.New(cfg.Database, registry)
	if err != nil {
		logger.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	logger.Infof("Connected to database: %s:%d/%s", cfg.Database.Host, cfg.Database.Port, cfg.Database.Name)

	sshPool := ssh.NewPool(ssh.PoolConfig{
		MaxIdleConnections:    cfg.SSH.MaxIdleConnections,
		MaxConnectionDuration: time.Duration(cfg.SSH.MaxConnectionDuration) * time.Second,
		ConnectionTimeout:     time.Duration(cfg.SSH.ConnectionTimeout) * time.Second,
		MaxRetries:            cfg.SSH.MaxRetries,
		RetryInterval:         time.Duration(cfg.SSH.RetryInterval) * time.Millisecond,
	}, logger.WithField("component", "ssh-pool"))

	logger.Infof("SSH pool initialized with max idle connections: %d", cfg.SSH.MaxIdleConnections)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grpcServer := grpc.NewServer(cfg.GRPC, database, logger.WithField("component", "grpc-server"))

	go func() {
		http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
		logger.Infof("Metrics server starting on %s", *metricsAddr)
		if err := http.ListenAndServe(*metricsAddr, nil); err != nil {
			logger.Errorf("Metrics server error: %v", err)
		}
	}()

	go func() {
		if err := grpcServer.Start(); err != nil {
			logger.Errorf("gRPC server error: %v", err)
			cancel()
		}
	}()

	logger.Infof("gRPC server listening on %s:%d", cfg.GRPC.Host, cfg.GRPC.Port)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	logger.Infof("Received signal %v, shutting down...", sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 30*time.Second)
	defer shutdownCancel()

	grpcServer.Stop(shutdownCtx)
	sshPool.Close()

	logger.Info("Server shutdown complete")
}

// Unused import suppression
var _ = fmt.Println
