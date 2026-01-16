/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"kube-static-pool-autoscaler/internal/config"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// DB wraps the sql.DB connection with additional functionality
type DB struct {
	*sql.DB
	logger  *logrus.Entry
	metrics *dbMetrics
}

// dbMetrics holds database metrics
type dbMetrics struct {
	queryDuration    *prometheus.HistogramVec
	connectionPool   *prometheus.GaugeVec
	activeOperations *prometheus.GaugeVec
}

// New creates a new database connection
func New(cfg config.DatabaseConfig, registry prometheus.Collector) (*DB, error) {
	db, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetimeDuration())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	metrics := newDBMetrics()
	prometheus.MustRegister(metrics.queryDuration)

	return &DB{
		DB:      db,
		logger:  logrus.WithField("component", "database"),
		metrics: metrics,
	}, nil
}

func newDBMetrics() *dbMetrics {
	return &dbMetrics{
		queryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ksa_db_query_duration_seconds",
			Help:    "Database query duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"query_type", "table"}),
		connectionPool: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ksa_db_connections",
			Help: "Database connection pool stats",
		}, []string{"state"}),
		activeOperations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ksa_db_active_operations",
			Help: "Number of active database operations",
		}, []string{"operation"}),
	}
}

// Metrics returns the database metrics
func (db *DB) Metrics() *dbMetrics {
	return db.metrics
}

// Close closes the database connection
func (db *DB) Close() error {
	db.logger.Info("Closing database connection")
	return db.DB.Close()
}

// Stats returns database connection pool statistics
func (db *DB) Stats() sql.DBStats {
	return db.DB.Stats()
}

// QueryRowContext wraps QueryRowContext with metrics
func (db *DB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := time.Now()
	row := db.DB.QueryRowContext(ctx, query, args...)
	duration := time.Since(start).Seconds()

	db.metrics.queryDuration.WithLabelValues("select", "unknown").Observe(duration)

	return row
}

// ExecContext wraps ExecContext with metrics
func (db *DB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := db.DB.ExecContext(ctx, query, args...)
	duration := time.Since(start).Seconds()

	db.metrics.queryDuration.WithLabelValues("exec", "unknown").Observe(duration)

	return result, err
}

// QueryContext wraps QueryContext with metrics
func (db *DB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := db.DB.QueryContext(ctx, query, args...)
	duration := time.Since(start).Seconds()

	db.metrics.queryDuration.WithLabelValues("query", "unknown").Observe(duration)

	return rows, err
}

// UpdateConnectionPoolMetrics updates the connection pool metrics
func (db *DB) UpdateConnectionPoolMetrics() {
	stats := db.Stats()

	db.metrics.connectionPool.WithLabelValues("open").Set(float64(stats.OpenConnections))
	db.metrics.connectionPool.WithLabelValues("in_use").Set(float64(stats.InUse))
	db.metrics.connectionPool.WithLabelValues("idle").Set(float64(stats.Idle))
}

// HealthCheck performs a database health check
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.PingContext(ctx)
}

// Ping is a convenience method for health checks
func (db *DB) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db.PingContext(ctx)
}
