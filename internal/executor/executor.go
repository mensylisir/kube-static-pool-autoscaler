/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kube-static-pool-autoscaler/internal/models"
	"kube-static-pool-autoscaler/internal/ssh"

	"github.com/sirupsen/logrus"
)

// OperationType represents the type of SSH operation
type OperationType string

const (
	OperationJoin   OperationType = "join"
	OperationReset  OperationType = "reset"
	OperationDrain  OperationType = "drain"
	OperationUnjoin OperationType = "unjoin"
)

// Executor handles SSH operations on machines
type Executor struct {
	sshPool *ssh.Pool
	logger  *logrus.Entry
}

// NewExecutor creates a new SSH executor
func NewExecutor(sshPool *ssh.Pool, logger *logrus.Entry) *Executor {
	return &Executor{
		sshPool: sshPool,
		logger:  logger.WithField("component", "executor"),
	}
}

// ExecuteOperation performs an SSH operation on a machine
func (e *Executor) ExecuteOperation(ctx context.Context, machine *models.Machine, opType OperationType) (*OperationResult, error) {
	e.logger.Infof("Executing %s operation on machine %s (%s)", opType, machine.Name, machine.IPAddress)

	client, err := e.sshPool.GetClient(ctx, machine.SSHUser, machine.IPAddress, machine.SSHPort, machine.SSHKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get SSH client: %w", err)
	}
	defer e.sshPool.ReturnClient(client)

	var cmd string

	switch opType {
	case OperationJoin:
		cmd = e.getJoinCommand(machine)
	case OperationReset:
		cmd = e.getResetCommand(machine)
	case OperationDrain:
		cmd = e.getDrainCommand(machine)
	case OperationUnjoin:
		cmd = e.getUnjoinCommand(machine)
	default:
		return nil, fmt.Errorf("unknown operation type: %s", opType)
	}

	e.logger.Debugf("Executing command: %s", cmd)

	output, err := e.sshPool.Execute(ctx, client, cmd)
	if err != nil {
		e.logger.Warnf("Command failed: %v, output: %s", err, output)
		return &OperationResult{
			Success:   false,
			Output:    output,
			Error:     err.Error(),
			MachineID: machine.ID,
			Operation: opType,
		}, nil
	}

	e.logger.Infof("Operation %s completed successfully on %s", opType, machine.IPAddress)

	return &OperationResult{
		Success:   true,
		Output:    output,
		MachineID: machine.ID,
		Operation: opType,
	}, nil
}

// OperationResult holds the result of an SSH operation
type OperationResult struct {
	Success   bool
	Output    string
	Error     string
	MachineID string
	Operation OperationType
	StartTime time.Time
	EndTime   time.Time
}

// Join commands

func (e *Executor) getJoinCommand(machine *models.Machine) string {
	// Check if kubelet is running
	cmd := `
if systemctl is-active --quiet kubelet; then
    echo "kubelet is running"
    exit 0
fi

echo "Starting kubelet service..."
sudo systemctl start kubelet || echo "Failed to start kubelet"

echo "Waiting for kubelet to be ready..."
for i in {1..60}; do
    if systemctl is-active --quiet kubelet; then
        echo "kubelet is now running"
        exit 0
    fi
    sleep 5
done

echo "kubelet did not start in time"
exit 1
`
	return cmd
}

func (e *Executor) getResetCommand(machine *models.Machine) string {
	cmd := `
echo "Resetting node..."

# Cordon the node first
kubectl cordon $(hostname) 2>/dev/null || true

# Drain the node
kubectl drain $(hostname) --ignore-daemonsets --delete-emptydir-data --force 2>/dev/null || true

echo "Node reset completed"
`
	return cmd
}

func (e *Executor) getDrainCommand(machine *models.Machine) string {
	cmd := `
echo "Draining node..."

# Cordon the node
kubectl cordon $(hostname) 2>/dev/null || echo "Already cordoned or kubectl not configured"

# Drain the node with all options
kubectl drain $(hostname) \
    --ignore-daemonsets \
    --delete-emptydir-data \
    --force \
    --timeout=600s 2>/dev/null || {
    echo "Drain completed with warnings"
}

echo "Node drain completed"
`
	return cmd
}

func (e *Executor) getUnjoinCommand(machine *models.Machine) string {
	cmd := `
echo "Unjoining node from cluster..."

# Stop kubelet
sudo systemctl stop kubelet 2>/dev/null || echo "kubelet not running"

# Reset kubelet
sudo kubeadm reset -f 2>/dev/null || echo "kubeadm reset not applicable"

# Remove CNI configuration
sudo rm -f /etc/cni/net.d/* 2>/dev/null || true

# Clean up kubelet data
sudo rm -rf /var/lib/kubelet/* 2>/dev/null || true

echo "Node unjoin completed"
`
	return cmd
}

// ExecuteScript executes a custom script on a machine
func (e *Executor) ExecuteScript(ctx context.Context, machine *models.Machine, script string, timeout time.Duration) (string, error) {
	e.logger.Infof("Executing custom script on machine %s (%s)", machine.Name, machine.IPAddress)

	client, err := e.sshPool.GetClient(ctx, machine.SSHUser, machine.IPAddress, machine.SSHPort, machine.SSHKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to get SSH client: %w", err)
	}
	defer e.sshPool.ReturnClient(client)

	output, err := e.sshPool.Execute(ctx, client, script)
	if err != nil {
		return output, fmt.Errorf("script execution failed: %w", err)
	}

	return output, nil
}

// CheckConnection checks if SSH connection to a machine is working
func (e *Executor) CheckConnection(ctx context.Context, machine *models.Machine) (bool, error) {
	client, err := e.sshPool.GetClient(ctx, machine.SSHUser, machine.IPAddress, machine.SSHPort, machine.SSHKeyPath)
	if err != nil {
		return false, fmt.Errorf("failed to connect: %w", err)
	}
	defer e.sshPool.ReturnClient(client)

	// Try a simple command
	output, err := e.sshPool.Execute(ctx, client, "echo 'connection ok'")
	if err != nil {
		return false, err
	}

	return strings.Contains(output, "connection ok"), nil
}

// ExecuteBatch executes the same operation on multiple machines
func (e *Executor) ExecuteBatch(ctx context.Context, machines []*models.Machine, opType OperationType) ([]*OperationResult, error) {
	e.logger.Infof("Executing %s operation on %d machines", opType, len(machines))

	results := make([]*OperationResult, 0, len(machines))

	for _, machine := range machines {
		result, err := e.ExecuteOperation(ctx, machine, opType)
		if err != nil {
			e.logger.Warnf("Operation failed for machine %s: %v", machine.ID, err)
			results = append(results, &OperationResult{
				Success:   false,
				Error:     err.Error(),
				MachineID: machine.ID,
				Operation: opType,
			})
		} else {
			results = append(results, result)
		}
	}

	return results, nil
}

// ExecuteWithRetry performs an operation with retry logic
func (e *Executor) ExecuteWithRetry(ctx context.Context, machine *models.Machine, opType OperationType, maxRetries int, retryInterval time.Duration) (*OperationResult, error) {
	var lastResult *OperationResult
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			e.logger.Infof("Retry attempt %d/%d for %s on %s", attempt, maxRetries, opType, machine.IPAddress)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryInterval):
			}
		}

		result, err := e.ExecuteOperation(ctx, machine, opType)
		if err != nil {
			lastErr = err
			lastResult = nil
			continue
		}

		if result.Success {
			return result, nil
		}

		lastResult = result
		lastErr = fmt.Errorf("operation failed: %s", result.Error)
	}

	if lastResult != nil {
		return lastResult, nil
	}

	return nil, lastErr
}
