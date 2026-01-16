/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// PoolConfig holds SSH connection pool configuration
type PoolConfig struct {
	MaxIdleConnections    int
	MaxConnectionDuration time.Duration
	ConnectionTimeout     time.Duration
	MaxRetries            int
	RetryInterval         time.Duration
}

// Pool manages SSH connections
type Pool struct {
	config        PoolConfig
	logger        *logrus.Entry
	mu            sync.RWMutex
	clients       map[string]*Client
	idleClients   []*Client
	activeClients int32
	totalClients  int32
	closed        bool

	metrics *poolMetrics
}

// Client wraps an SSH client with connection metadata
type Client struct {
	conn       *ssh.Client
	user       string
	host       string
	port       int
	createdAt  time.Time
	lastUsedAt time.Time
	usageCount int32
	inUse      bool
	isFaulty   bool
}

// poolMetrics holds SSH pool metrics
type poolMetrics struct {
	connectionDuration *prometheus.HistogramVec
	connectionAttempts *prometheus.CounterVec
	activeConnections  prometheus.Gauge
	totalConnections   prometheus.Gauge
	operationDuration  *prometheus.HistogramVec
	failedConnections  *prometheus.CounterVec
}

// NewPool creates a new SSH connection pool
func NewPool(config PoolConfig, logger *logrus.Entry) *Pool {
	pool := &Pool{
		config:      config,
		logger:      logger,
		clients:     make(map[string]*Client),
		idleClients: make([]*Client, 0, config.MaxIdleConnections),
		metrics:     newPoolMetrics(),
	}

	prometheus.MustRegister(pool.metrics.connectionDuration)
	prometheus.MustRegister(pool.metrics.connectionAttempts)
	prometheus.MustRegister(pool.metrics.activeConnections)
	prometheus.MustRegister(pool.metrics.totalConnections)
	prometheus.MustRegister(pool.metrics.operationDuration)
	prometheus.MustRegister(pool.metrics.failedConnections)

	go pool.cleanupLoop()

	return pool
}

func newPoolMetrics() *poolMetrics {
	return &poolMetrics{
		connectionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ksa_ssh_connection_duration_seconds",
			Help:    "SSH connection duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"host"}),
		connectionAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ksa_ssh_connection_attempts_total",
			Help: "Total number of SSH connection attempts",
		}, []string{"host", "status"}),
		activeConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ksa_ssh_active_connections",
			Help: "Number of active SSH connections",
		}),
		totalConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ksa_ssh_total_connections",
			Help: "Total number of SSH connections created",
		}),
		operationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ksa_ssh_operation_duration_seconds",
			Help:    "SSH operation duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"host", "operation"}),
		failedConnections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ksa_ssh_failed_connections_total",
			Help: "Total number of failed SSH connections",
		}, []string{"host", "reason"}),
	}
}

var (
	activeConnections int32
	totalConnections  int32
)

// GetClient retrieves or creates an SSH client for the given host
func (p *Pool) GetClient(ctx context.Context, user, host string, port int, keyPath string) (*Client, error) {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, fmt.Errorf("pool is closed")
	}

	clientKey := fmt.Sprintf("%s@%s:%d", user, host, port)

	for i := len(p.idleClients) - 1; i >= 0; i-- {
		c := p.idleClients[i]
		if c.user == user && c.host == host && c.port == port && !c.isFaulty {
			p.idleClients = append(p.idleClients[:i], p.idleClients[i+1:]...)
			c.inUse = true
			p.mu.RUnlock()
			p.logger.Debugf("Reused SSH client from pool for %s", clientKey)
			return c, nil
		}
	}
	p.mu.RUnlock()

	return p.createClient(ctx, user, host, port, keyPath)
}

// createClient establishes a new SSH connection
func (p *Pool) createClient(ctx context.Context, user, host string, port int, keyPath string) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, fmt.Errorf("pool is closed")
	}

	clientKey := fmt.Sprintf("%s@%s:%d", user, host, port)

	if existing, ok := p.clients[clientKey]; ok && !existing.inUse && !existing.isFaulty {
		existing.inUse = true
		return existing, nil
	}

	start := time.Now()
	conn, err := p.connect(ctx, user, host, port, keyPath)
	duration := time.Since(start).Seconds()

	if err != nil {
		p.metrics.connectionAttempts.WithLabelValues(host, "failed").Inc()
		p.metrics.failedConnections.WithLabelValues(host, err.Error()).Inc()
		return nil, fmt.Errorf("failed to connect to %s: %w", clientKey, err)
	}

	p.metrics.connectionAttempts.WithLabelValues(host, "success").Inc()
	p.metrics.connectionDuration.WithLabelValues(host).Observe(duration)

	client := &Client{
		conn:       conn,
		user:       user,
		host:       host,
		port:       port,
		createdAt:  time.Now(),
		lastUsedAt: time.Now(),
		inUse:      true,
		usageCount: 1,
	}

	p.clients[clientKey] = client
	atomic.AddInt32(&totalConnections, 1)
	atomic.AddInt32(&activeConnections, 1)

	p.metrics.totalConnections.Set(float64(atomic.LoadInt32(&totalConnections)))

	p.logger.Debugf("Created new SSH client for %s", clientKey)

	return client, nil
}

// connect establishes the actual SSH connection
func (p *Pool) connect(ctx context.Context, user, host string, port int, keyPath string) (*ssh.Client, error) {
	var authMethods []ssh.AuthMethod

	if keyPath != "" {
		key, err := loadPrivateKey(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load private key: %w", err)
		}
		authMethods = append(authMethods, key)
	}

	if agentAuth, err := loadSSHAgentAuth(); err == nil {
		authMethods = append(authMethods, agentAuth)
	}

	authMethods = append(authMethods, ssh.PasswordCallback(func() (string, error) {
		return "", fmt.Errorf("password auth not supported")
	}))

	hostKeyCallback, err := knownhosts.New("/etc/ssh/ssh_known_hosts")
	if err != nil {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         p.config.ConnectionTimeout,
	}

	var lastErr error
	for attempt := 0; attempt <= p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(p.config.RetryInterval):
			}
		}

		conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		p.logger.Warnf("SSH connection attempt %d/%d to %s:%d failed: %v",
			attempt+1, p.config.MaxRetries, host, port, err)
	}

	return nil, lastErr
}

// ReturnClient returns a client to the pool
func (p *Pool) ReturnClient(client *Client) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client == nil || p.closed {
		return
	}

	client.inUse = false
	client.lastUsedAt = time.Now()

	if client.isFaulty || !isConnectionAlive(client.conn) {
		clientKey := fmt.Sprintf("%s@%s:%d", client.user, client.host, client.port)
		delete(p.clients, clientKey)
		atomic.AddInt32(&activeConnections, -1)
		p.metrics.activeConnections.Set(float64(atomic.LoadInt32(&activeConnections)))
		p.logger.Debugf("Removed faulty SSH client for %s", clientKey)
		return
	}

	if len(p.idleClients) < p.config.MaxIdleConnections {
		p.idleClients = append(p.idleClients, client)
	} else {
		clientKey := fmt.Sprintf("%s@%s:%d", client.user, client.host, client.port)
		delete(p.clients, clientKey)
		client.conn.Close()
		atomic.AddInt32(&activeConnections, -1)
		p.metrics.activeConnections.Set(float64(atomic.LoadInt32(&activeConnections)))
	}
}

// Execute runs a command on the remote host
func (p *Pool) Execute(ctx context.Context, client *Client, cmd string) (string, error) {
	start := time.Now()

	session, err := client.conn.NewSession()
	if err != nil {
		p.metrics.operationDuration.WithLabelValues(client.host, "session_create").Observe(time.Since(start).Seconds())
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(cmd)
	duration := time.Since(start).Seconds()
	p.metrics.operationDuration.WithLabelValues(client.host, "execute").Observe(duration)

	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}

	return string(output), nil
}

// Close closes the pool and all connections
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	p.closed = true

	for _, client := range p.clients {
		client.conn.Close()
		atomic.AddInt32(&activeConnections, -1)
	}

	p.clients = make(map[string]*Client)
	p.idleClients = make([]*Client, 0)

	p.metrics.activeConnections.Set(0)

	p.logger.Info("SSH pool closed")
}

// cleanupLoop periodically removes stale connections
func (p *Pool) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}

		now := time.Now()
		toRemove := make([]*Client, 0)

		for _, client := range p.clients {
			if client.inUse {
				continue
			}

			age := now.Sub(client.createdAt)
			idleTime := now.Sub(client.lastUsedAt)

			if age > p.config.MaxConnectionDuration || idleTime > 2*p.config.MaxConnectionDuration {
				toRemove = append(toRemove, client)
			}
		}

		for _, client := range toRemove {
			clientKey := fmt.Sprintf("%s@%s:%d", client.user, client.host, client.port)
			delete(p.clients, clientKey)
			client.conn.Close()
			atomic.AddInt32(&activeConnections, -1)

			for i, idle := range p.idleClients {
				if idle == client {
					p.idleClients = append(p.idleClients[:i], p.idleClients[i+1:]...)
					break
				}
			}
		}

		p.mu.Unlock()

		if len(toRemove) > 0 {
			p.metrics.activeConnections.Set(float64(atomic.LoadInt32(&activeConnections)))
			p.logger.Debugf("Cleaned up %d stale SSH connections", len(toRemove))
		}
	}
}

// Stats returns pool statistics
func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PoolStats{
		TotalClients:  int(atomic.LoadInt32(&totalConnections)),
		ActiveClients: int(atomic.LoadInt32(&activeConnections)),
		IdleClients:   len(p.idleClients),
	}
}

// PoolStats holds pool statistics
type PoolStats struct {
	TotalClients  int
	ActiveClients int
	IdleClients   int
}

// loadPrivateKey loads an SSH private key from file
func loadPrivateKey(path string) (ssh.AuthMethod, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}

	return ssh.PublicKeys(signer), nil
}

// loadSSHAgentAuth loads authentication from SSH agent
func loadSSHAgentAuth() (ssh.AuthMethod, error) {
	conn := agent.NewClient(&agentConn{})

	signers, err := conn.Signers()
	if err != nil {
		return nil, err
	}

	return ssh.PublicKeys(signers...), nil
}

// isConnectionAlive checks if the SSH connection is still alive
func isConnectionAlive(conn *ssh.Client) bool {
	_, _, err := conn.SendRequest("keepalive@golang.org", true, nil)
	return err == nil
}

// agentConn implements net.Conn for SSH agent
type agentConn struct{}

func (a *agentConn) Read(b []byte) (n int, err error)   { return 0, nil }
func (a *agentConn) Write(b []byte) (n int, err error)  { return len(b), nil }
func (a *agentConn) Close() error                       { return nil }
func (a *agentConn) LocalAddr() net.Addr                { return nil }
func (a *agentConn) RemoteAddr() net.Addr               { return nil }
func (a *agentConn) SetDeadline(t time.Time) error      { return nil }
func (a *agentConn) SetReadDeadline(t time.Time) error  { return nil }
func (a *agentConn) SetWriteDeadline(t time.Time) error { return nil }
