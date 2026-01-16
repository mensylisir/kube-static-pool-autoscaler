/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package db

import (
	"context"
	"fmt"
	"time"

	"database/sql"

	"kube-static-pool-autoscaler/internal/models"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

// MachineRepository handles database operations for machines
type MachineRepository struct {
	db     *DB
	logger *logrus.Entry
}

// NewMachineRepository creates a new machine repository
func NewMachineRepository(database *DB) *MachineRepository {
	return &MachineRepository{
		db:     database,
		logger: database.logger.WithField("repository", "machines"),
	}
}

// Create inserts a new machine into the database
func (r *MachineRepository) Create(ctx context.Context, machine *models.Machine) error {
	if machine.ID == "" {
		machine.ID = uuid.New().String()
	}

	query := sq.Insert("machines").
		Columns(
			"id", "name", "ip_address", "ssh_user", "ssh_port", "ssh_key_path",
			"state", "nodepool_id", "labels", "tains", "cpu", "memory",
			"gpu", "gpu_count", "maintenance_mode",
		).
		Values(
			machine.ID, machine.Name, machine.IPAddress, machine.SSHUser, machine.SSHPort,
			machine.SSHKeyPath, machine.State, machine.NodepoolID, machine.Labels,
			machine.Taints, machine.CPU, machine.Memory, machine.GPU, machine.GPUCount,
			machine.MaintenanceMode,
		).
		Suffix("RETURNING created_at, updated_at")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(&machine.CreatedAt, &machine.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert machine: %w", err)
	}

	machine.LastSeenAt = time.Now()
	r.logger.Infof("Created machine %s at %s", machine.ID, machine.IPAddress)
	return nil
}

// GetByID retrieves a machine by its ID
func (r *MachineRepository) GetByID(ctx context.Context, id string) (*models.Machine, error) {
	query := sq.Select("*").
		From("machines").
		Where("id = ?", id).
		Limit(1)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	machine := &models.Machine{}
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(
		&machine.ID, &machine.Name, &machine.IPAddress, &machine.SSHUser,
		&machine.SSHPort, &machine.SSHKeyPath, &machine.State, &machine.NodepoolID,
		&machine.Labels, &machine.Taints, &machine.CPU, &machine.Memory,
		&machine.GPU, &machine.GPUCount, &machine.CreatedAt, &machine.UpdatedAt,
		&machine.LastSeenAt, &machine.MaintenanceMode, &machine.FaultReason,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get machine: %w", err)
	}

	return machine, nil
}

// GetByIP retrieves a machine by its IP address
func (r *MachineRepository) GetByIP(ctx context.Context, ip string) (*models.Machine, error) {
	query := sq.Select("*").
		From("machines").
		Where("ip_address = ?", ip).
		Limit(1)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	machine := &models.Machine{}
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(
		&machine.ID, &machine.Name, &machine.IPAddress, &machine.SSHUser,
		&machine.SSHPort, &machine.SSHKeyPath, &machine.State, &machine.NodepoolID,
		&machine.Labels, &machine.Taints, &machine.CPU, &machine.Memory,
		&machine.GPU, &machine.GPUCount, &machine.CreatedAt, &machine.UpdatedAt,
		&machine.LastSeenAt, &machine.MaintenanceMode, &machine.FaultReason,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get machine by IP: %w", err)
	}

	return machine, nil
}

// GetByState retrieves all machines in a specific state
func (r *MachineRepository) GetByState(ctx context.Context, state models.MachineState) ([]*models.Machine, error) {
	query := sq.Select("*").
		From("machines").
		Where("state = ?", state).
		OrderBy("created_at ASC")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query machines: %w", err)
	}
	defer rows.Close()

	var machines []*models.Machine
	for rows.Next() {
		machine := &models.Machine{}
		err := rows.Scan(
			&machine.ID, &machine.Name, &machine.IPAddress, &machine.SSHUser,
			&machine.SSHPort, &machine.SSHKeyPath, &machine.State, &machine.NodepoolID,
			&machine.Labels, &machine.Taints, &machine.CPU, &machine.Memory,
			&machine.GPU, &machine.GPUCount, &machine.CreatedAt, &machine.UpdatedAt,
			&machine.LastSeenAt, &machine.MaintenanceMode, &machine.FaultReason,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan machine: %w", err)
		}
		machines = append(machines, machine)
	}

	return machines, rows.Err()
}

// GetByNodepool retrieves all machines in a nodepool
func (r *MachineRepository) GetByNodepool(ctx context.Context, nodepoolID string) ([]*models.Machine, error) {
	query := sq.Select("*").
		From("machines").
		Where("nodepool_id = ?", nodepoolID).
		OrderBy("created_at ASC")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query machines: %w", err)
	}
	defer rows.Close()

	var machines []*models.Machine
	for rows.Next() {
		machine := &models.Machine{}
		err := rows.Scan(
			&machine.ID, &machine.Name, &machine.IPAddress, &machine.SSHUser,
			&machine.SSHPort, &machine.SSHKeyPath, &machine.State, &machine.NodepoolID,
			&machine.Labels, &machine.Taints, &machine.CPU, &machine.Memory,
			&machine.GPU, &machine.GPUCount, &machine.CreatedAt, &machine.UpdatedAt,
			&machine.LastSeenAt, &machine.MaintenanceMode, &machine.FaultReason,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan machine: %w", err)
		}
		machines = append(machines, machine)
	}

	return machines, rows.Err()
}

// GetAll retrieves all machines with optional filters
func (r *MachineRepository) GetAll(ctx context.Context, limit, offset int) ([]*models.Machine, error) {
	query := sq.Select("*").
		From("machines").
		OrderBy("created_at DESC").
		Limit(uint64(limit)).
		Offset(uint64(offset))

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query machines: %w", err)
	}
	defer rows.Close()

	var machines []*models.Machine
	for rows.Next() {
		machine := &models.Machine{}
		err := rows.Scan(
			&machine.ID, &machine.Name, &machine.IPAddress, &machine.SSHUser,
			&machine.SSHPort, &machine.SSHKeyPath, &machine.State, &machine.NodepoolID,
			&machine.Labels, &machine.Taints, &machine.CPU, &machine.Memory,
			&machine.GPU, &machine.GPUCount, &machine.CreatedAt, &machine.UpdatedAt,
			&machine.LastSeenAt, &machine.MaintenanceMode, &machine.FaultReason,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan machine: %w", err)
		}
		machines = append(machines, machine)
	}

	return machines, rows.Err()
}

// Update updates an existing machine
func (r *MachineRepository) Update(ctx context.Context, machine *models.Machine) error {
	query := sq.Update("machines").
		Set("name", machine.Name).
		Set("ip_address", machine.IPAddress).
		Set("ssh_user", machine.SSHUser).
		Set("ssh_port", machine.SSHPort).
		Set("ssh_key_path", machine.SSHKeyPath).
		Set("state", machine.State).
		Set("nodepool_id", machine.NodepoolID).
		Set("labels", machine.Labels).
		Set("tains", machine.Taints).
		Set("cpu", machine.CPU).
		Set("memory", machine.Memory).
		Set("gpu", machine.GPU).
		Set("gpu_count", machine.GPUCount).
		Set("maintenance_mode", machine.MaintenanceMode).
		Set("fault_reason", machine.FaultReason).
		Where("id = ?", machine.ID)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to update machine: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("machine not found: %s", machine.ID)
	}

	machine.UpdatedAt = time.Now()
	r.logger.Infof("Updated machine %s", machine.ID)
	return nil
}

// UpdateState updates only the state of a machine
func (r *MachineRepository) UpdateState(ctx context.Context, id string, state models.MachineState) error {
	query := sq.Update("machines").
		Set("state", state).
		Set("updated_at", sq.Expr("NOW()")).
		Where("id = ?", id)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to update machine state: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("machine not found: %s", id)
	}

	r.logger.Infof("Updated machine %s state to %s", id, state)
	return nil
}

// AssignToNodepool assigns a machine to a nodepool
func (r *MachineRepository) AssignToNodepool(ctx context.Context, machineID, nodepoolID string) error {
	query := sq.Update("machines").
		Set("nodepool_id", nodepoolID).
		Set("state", models.MachineStateInUse).
		Set("updated_at", sq.Expr("NOW()")).
		Where("id = ?", machineID)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to assign machine to nodepool: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("machine not found: %s", machineID)
	}

	r.logger.Infof("Assigned machine %s to nodepool %s", machineID, nodepoolID)
	return nil
}

// UnassignFromNodepool unassigns a machine from its nodepool
func (r *MachineRepository) UnassignFromNodepool(ctx context.Context, machineID string) error {
	query := sq.Update("machines").
		Set("nodepool_id", nil).
		Set("state", models.MachineStateAvailable).
		Set("updated_at", sq.Expr("NOW()")).
		Where("id = ?", machineID)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to unassign machine from nodepool: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("machine not found: %s", machineID)
	}

	r.logger.Infof("Unassigned machine %s from nodepool", machineID)
	return nil
}

// Delete removes a machine from the database
func (r *MachineRepository) Delete(ctx context.Context, id string) error {
	query := sq.Delete("machines").
		Where("id = ?", id)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to delete machine: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("machine not found: %s", id)
	}

	r.logger.Infof("Deleted machine %s", id)
	return nil
}

// Count returns the total number of machines
func (r *MachineRepository) Count(ctx context.Context) (int, error) {
	query := sq.Select("COUNT(*)").From("machines")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return 0, fmt.Errorf("failed to build query: %w", err)
	}

	var count int
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count machines: %w", err)
	}

	return count, nil
}

// BulkCreate inserts multiple machines in a single transaction
func (r *MachineRepository) BulkCreate(ctx context.Context, machines []*models.Machine) error {
	if len(machines) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, pq.CopyIn(
		"machines",
		"id", "name", "ip_address", "ssh_user", "ssh_port", "ssh_key_path",
		"state", "nodepool_id", "labels", "tains", "cpu", "memory",
		"gpu", "gpu_count", "maintenance_mode",
	))
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, m := range machines {
		if m.ID == "" {
			m.ID = uuid.New().String()
		}
		_, err := stmt.ExecContext(ctx,
			m.ID, m.Name, m.IPAddress, m.SSHUser, m.SSHPort, m.SSHKeyPath,
			m.State, m.NodepoolID, m.Labels, m.Taints, m.CPU, m.Memory,
			m.GPU, m.GPUCount, m.MaintenanceMode,
		)
		if err != nil {
			return fmt.Errorf("failed to insert machine: %w", err)
		}
	}

	_, err = stmt.ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to flush batch: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	r.logger.Infof("Bulk created %d machines", len(machines))
	return nil
}
