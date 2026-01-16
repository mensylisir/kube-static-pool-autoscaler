/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package db

import (
	"context"
	"database/sql"
	"fmt"

	"kube-static-pool-autoscaler/internal/models"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// NodepoolRepository handles database operations for nodepools
type NodepoolRepository struct {
	db     *DB
	logger *logrus.Entry
}

// NewNodepoolRepository creates a new nodepool repository
func NewNodepoolRepository(database *DB) *NodepoolRepository {
	return &NodepoolRepository{
		db:     database,
		logger: database.logger.WithField("repository", "nodepools"),
	}
}

// Create inserts a new nodepool into the database
func (r *NodepoolRepository) Create(ctx context.Context, nodepool *models.Nodepool) error {
	if nodepool.ID == "" {
		nodepool.ID = uuid.New().String()
	}

	query := sq.Insert("nodepools").
		Columns(
			"id", "name", "min_size", "max_size", "state",
			"labels", "tains", "cpu", "memory", "gpu", "gpu_count", "instance_type",
		).
		Values(
			nodepool.ID, nodepool.Name, nodepool.MinSize, nodepool.MaxSize, nodepool.State,
			nodepool.Labels, nodepool.Taints, nodepool.CPU, nodepool.Memory,
			nodepool.GPU, nodepool.GPUCount, nodepool.InstanceType,
		).
		Suffix("RETURNING created_at, updated_at")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(&nodepool.CreatedAt, &nodepool.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert nodepool: %w", err)
	}

	r.logger.Infof("Created nodepool %s (%s)", nodepool.ID, nodepool.Name)
	return nil
}

// GetByID retrieves a nodepool by its ID
func (r *NodepoolRepository) GetByID(ctx context.Context, id string) (*models.Nodepool, error) {
	query := sq.Select("*").
		From("nodepools").
		Where("id = ?", id).
		Limit(1)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	nodepool := &models.Nodepool{}
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(
		&nodepool.ID, &nodepool.Name, &nodepool.MinSize, &nodepool.MaxSize,
		&nodepool.State, &nodepool.Labels, &nodepool.Taints, &nodepool.CPU,
		&nodepool.Memory, &nodepool.GPU, &nodepool.GPUCount, &nodepool.InstanceType,
		&nodepool.CreatedAt, &nodepool.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get nodepool: %w", err)
	}

	return nodepool, nil
}

// GetByName retrieves a nodepool by its name
func (r *NodepoolRepository) GetByName(ctx context.Context, name string) (*models.Nodepool, error) {
	query := sq.Select("*").
		From("nodepools").
		Where("name = ?", name).
		Limit(1)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	nodepool := &models.Nodepool{}
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(
		&nodepool.ID, &nodepool.Name, &nodepool.MinSize, &nodepool.MaxSize,
		&nodepool.State, &nodepool.Labels, &nodepool.Taints, &nodepool.CPU,
		&nodepool.Memory, &nodepool.GPU, &nodepool.GPUCount, &nodepool.InstanceType,
		&nodepool.CreatedAt, &nodepool.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get nodepool by name: %w", err)
	}

	return nodepool, nil
}

// GetByState retrieves all nodepools in a specific state
func (r *NodepoolRepository) GetByState(ctx context.Context, state models.NodepoolState) ([]*models.Nodepool, error) {
	query := sq.Select("*").
		From("nodepools").
		Where("state = ?", state).
		OrderBy("name ASC")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodepools: %w", err)
	}
	defer rows.Close()

	var nodepools []*models.Nodepool
	for rows.Next() {
		nodepool := &models.Nodepool{}
		err := rows.Scan(
			&nodepool.ID, &nodepool.Name, &nodepool.MinSize, &nodepool.MaxSize,
			&nodepool.State, &nodepool.Labels, &nodepool.Taints, &nodepool.CPU,
			&nodepool.Memory, &nodepool.GPU, &nodepool.GPUCount, &nodepool.InstanceType,
			&nodepool.CreatedAt, &nodepool.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan nodepool: %w", err)
		}
		nodepools = append(nodepools, nodepool)
	}

	return nodepools, rows.Err()
}

// GetAll retrieves all nodepools
func (r *NodepoolRepository) GetAll(ctx context.Context, limit, offset int) ([]*models.Nodepool, error) {
	query := sq.Select("*").
		From("nodepools").
		OrderBy("name ASC").
		Limit(uint64(limit)).
		Offset(uint64(offset))

	queryStr, args, err := query.ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodepools: %w", err)
	}
	defer rows.Close()

	var nodepools []*models.Nodepool
	for rows.Next() {
		nodepool := &models.Nodepool{}
		err := rows.Scan(
			&nodepool.ID, &nodepool.Name, &nodepool.MinSize, &nodepool.MaxSize,
			&nodepool.State, &nodepool.Labels, &nodepool.Taints, &nodepool.CPU,
			&nodepool.Memory, &nodepool.GPU, &nodepool.GPUCount, &nodepool.InstanceType,
			&nodepool.CreatedAt, &nodepool.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan nodepool: %w", err)
		}
		nodepools = append(nodepools, nodepool)
	}

	return nodepools, rows.Err()
}

// Update updates an existing nodepool
func (r *NodepoolRepository) Update(ctx context.Context, nodepool *models.Nodepool) error {
	query := sq.Update("nodepools").
		Set("name", nodepool.Name).
		Set("min_size", nodepool.MinSize).
		Set("max_size", nodepool.MaxSize).
		Set("state", nodepool.State).
		Set("labels", nodepool.Labels).
		Set("tains", nodepool.Taints).
		Set("cpu", nodepool.CPU).
		Set("memory", nodepool.Memory).
		Set("gpu", nodepool.GPU).
		Set("gpu_count", nodepool.GPUCount).
		Set("instance_type", nodepool.InstanceType).
		Where("id = ?", nodepool.ID)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to update nodepool: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("nodepool not found: %s", nodepool.ID)
	}

	r.logger.Infof("Updated nodepool %s", nodepool.ID)
	return nil
}

// UpdateState updates only the state of a nodepool
func (r *NodepoolRepository) UpdateState(ctx context.Context, id string, state models.NodepoolState) error {
	query := sq.Update("nodepools").
		Set("state", state).
		Set("updated_at", sq.Expr("NOW()")).
		Where("id = ?", id)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to update nodepool state: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("nodepool not found: %s", id)
	}

	r.logger.Infof("Updated nodepool %s state to %s", id, state)
	return nil
}

// Delete removes a nodepool from the database
func (r *NodepoolRepository) Delete(ctx context.Context, id string) error {
	query := sq.Delete("nodepools").
		Where("id = ?", id)

	queryStr, args, err := query.ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.db.ExecContext(ctx, queryStr, args...)
	if err != nil {
		return fmt.Errorf("failed to delete nodepool: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("nodepool not found: %s", id)
	}

	r.logger.Infof("Deleted nodepool %s", id)
	return nil
}

// Count returns the total number of nodepools
func (r *NodepoolRepository) Count(ctx context.Context) (int, error) {
	query := sq.Select("COUNT(*)").From("nodepools")

	queryStr, args, err := query.ToSql()
	if err != nil {
		return 0, fmt.Errorf("failed to build query: %w", err)
	}

	var count int
	err = r.db.QueryRowContext(ctx, queryStr, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count nodepools: %w", err)
	}

	return count, nil
}

// GetMachineCount returns the number of machines in a nodepool
func (r *NodepoolRepository) GetMachineCount(ctx context.Context, nodepoolID string) (int, error) {
	query := sq.Select("COUNT(*)").
		From("machines").
		Where("nodepool_id = ?", nodepoolID)

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

// CanScaleUp checks if a nodepool can scale up
func (r *NodepoolRepository) CanScaleUp(ctx context.Context, nodepoolID string) (bool, error) {
	nodepool, err := r.GetByID(ctx, nodepoolID)
	if err != nil {
		return false, err
	}
	if nodepool == nil {
		return false, fmt.Errorf("nodepool not found: %s", nodepoolID)
	}

	if nodepool.State != models.NodepoolStateActive {
		return false, nil
	}

	machineCount, err := r.GetMachineCount(ctx, nodepoolID)
	if err != nil {
		return false, err
	}

	return machineCount < nodepool.MaxSize, nil
}

// CanScaleDown checks if a nodepool can scale down
func (r *NodepoolRepository) CanScaleDown(ctx context.Context, nodepoolID string) (bool, error) {
	nodepool, err := r.GetByID(ctx, nodepoolID)
	if err != nil {
		return false, err
	}
	if nodepool == nil {
		return false, fmt.Errorf("nodepool not found: %s", nodepoolID)
	}

	if nodepool.State != models.NodepoolStateActive {
		return false, nil
	}

	machineCount, err := r.GetMachineCount(ctx, nodepoolID)
	if err != nil {
		return false, err
	}

	return machineCount > nodepool.MinSize, nil
}

// GetActiveNodepools returns all active nodepools
func (r *NodepoolRepository) GetActiveNodepools(ctx context.Context) ([]*models.Nodepool, error) {
	return r.GetByState(ctx, models.NodepoolStateActive)
}
