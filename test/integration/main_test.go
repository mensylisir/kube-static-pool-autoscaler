/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"kube-static-pool-autoscaler/internal/config"
	"kube-static-pool-autoscaler/internal/db"
	"kube-static-pool-autoscaler/internal/models"

	_ "github.com/lib/pq"
)

// TestDatabaseIntegration tests database operations
func TestDatabaseIntegration(t *testing.T) {
	// Skip if running in CI without database
	if os.Getenv("INTEGRATION_TEST") != "true" && os.Getenv("POSTGRES_HOST") == "" {
		t.Skip("Skipping integration test. Set INTEGRATION_TEST=true or POSTGRES_HOST to run")
	}

	// Get database connection from environment
	host := getEnv("POSTGRES_HOST", "localhost")
	port := getEnvInt("POSTGRES_PORT", 5432)
	name := getEnv("POSTGRES_DB", "ksa")
	user := getEnv("POSTGRES_USER", "ksa")
	password := getEnv("POSTGRES_PASSWORD", "ksa")

	cfg := config.DatabaseConfig{
		Host:            host,
		Port:            port,
		Name:            name,
		User:            user,
		Password:        password,
		SSLMode:         "disable",
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 300,
	}

	database, err := db.New(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Test health check
	if err := database.HealthCheck(ctx); err != nil {
		t.Errorf("Health check failed: %v", err)
	}

	// Test machine repository
	machineRepo := db.NewMachineRepository(database)

	// Create a test machine
	machine := &models.Machine{
		Name:      "test-machine-" + randomString(8),
		IPAddress: "192.168.1." + randomString(3),
		SSHUser:   "root",
		SSHPort:   22,
		State:     models.MachineStateAvailable,
		CPU:       4,
		Memory:    8 * 1024 * 1024 * 1024, // 8GB
	}

	err = machineRepo.Create(ctx, machine)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Verify machine was created
	retrieved, err := machineRepo.GetByID(ctx, machine.ID)
	if err != nil {
		t.Fatalf("Failed to get machine by ID: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Machine not found after creation")
	}
	if retrieved.Name != machine.Name {
		t.Errorf("Machine name mismatch: expected %s, got %s", machine.Name, retrieved.Name)
	}

	// Test GetByIP
	byIP, err := machineRepo.GetByIP(ctx, machine.IPAddress)
	if err != nil {
		t.Fatalf("Failed to get machine by IP: %v", err)
	}
	if byIP == nil || byIP.ID != machine.ID {
		t.Error("Machine by IP not found")
	}

	// Test GetByState
	machines, err := machineRepo.GetByState(ctx, models.MachineStateAvailable)
	if err != nil {
		t.Fatalf("Failed to get machines by state: %v", err)
	}
	if len(machines) == 0 {
		t.Error("Expected at least one machine in available state")
	}

	// Test UpdateState
	err = machineRepo.UpdateState(ctx, machine.ID, models.MachineStateJoining)
	if err != nil {
		t.Fatalf("Failed to update machine state: %v", err)
	}

	updated, _ := machineRepo.GetByID(ctx, machine.ID)
	if updated.State != models.MachineStateJoining {
		t.Errorf("Expected state joining, got %s", updated.State)
	}

	// Test Count
	count, err := machineRepo.Count(ctx)
	if err != nil {
		t.Fatalf("Failed to count machines: %v", err)
	}
	if count < 1 {
		t.Error("Expected at least one machine")
	}

	// Cleanup - delete the test machine
	err = machineRepo.Delete(ctx, machine.ID)
	if err != nil {
		t.Errorf("Failed to delete test machine: %v", err)
	}
}

// TestNodepoolIntegration tests nodepool operations
func TestNodepoolIntegration(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "true" && os.Getenv("POSTGRES_HOST") == "" {
		t.Skip("Skipping integration test. Set INTEGRATION_TEST=true or POSTGRES_HOST to run")
	}

	host := getEnv("POSTGRES_HOST", "localhost")
	port := getEnvInt("POSTGRES_PORT", 5432)
	name := getEnv("POSTGRES_DB", "ksa")
	user := getEnv("POSTGRES_USER", "ksa")
	password := getEnv("POSTGRES_PASSWORD", "ksa")

	cfg := config.DatabaseConfig{
		Host:            host,
		Port:            port,
		Name:            name,
		User:            user,
		Password:        password,
		SSLMode:         "disable",
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 300,
	}

	database, err := db.New(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	nodepoolRepo := db.NewNodepoolRepository(database)

	// Create a test nodepool
	nodepool := &models.Nodepool{
		Name:    "test-nodepool-" + randomString(8),
		MinSize: 0,
		MaxSize: 10,
		State:   models.NodepoolStateActive,
	}

	err = nodepoolRepo.Create(ctx, nodepool)
	if err != nil {
		t.Fatalf("Failed to create nodepool: %v", err)
	}

	// Verify nodepool was created
	retrieved, err := nodepoolRepo.GetByID(ctx, nodepool.ID)
	if err != nil {
		t.Fatalf("Failed to get nodepool by ID: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Nodepool not found after creation")
	}

	// Test GetByName
	byName, err := nodepoolRepo.GetByName(ctx, nodepool.Name)
	if err != nil {
		t.Fatalf("Failed to get nodepool by name: %v", err)
	}
	if byName == nil || byName.ID != nodepool.ID {
		t.Error("Nodepool by name not found")
	}

	// Test GetAll
	nodepools, err := nodepoolRepo.GetAll(ctx, 100, 0)
	if err != nil {
		t.Fatalf("Failed to get all nodepools: %v", err)
	}
	if len(nodepools) == 0 {
		t.Error("Expected at least one nodepool")
	}

	// Test CanScaleUp/CanScaleDown
	canUp, err := nodepoolRepo.CanScaleUp(ctx, nodepool.ID)
	if err != nil {
		t.Fatalf("CanScaleUp failed: %v", err)
	}
	if !canUp {
		t.Error("Expected CanScaleUp to be true")
	}

	canDown, err := nodepoolRepo.CanScaleDown(ctx, nodepool.ID)
	if err != nil {
		t.Fatalf("CanScaleDown failed: %v", err)
	}
	if !canDown {
		t.Error("Expected CanScaleDown to be true (at min size)")
	}

	// Cleanup
	err = nodepoolRepo.Delete(ctx, nodepool.ID)
	if err != nil {
		t.Errorf("Failed to delete test nodepool: %v", err)
	}
}

// TestMachineAssignmentIntegration tests machine assignment to nodepools
func TestMachineAssignmentIntegration(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "true" && os.Getenv("POSTGRES_HOST") == "" {
		t.Skip("Skipping integration test. Set INTEGRATION_TEST=true or POSTGRES_HOST to run")
	}

	host := getEnv("POSTGRES_HOST", "localhost")
	port := getEnvInt("POSTGRES_PORT", 5432)
	name := getEnv("POSTGRES_DB", "ksa")
	user := getEnv("POSTGRES_USER", "ksa")
	password := getEnv("POSTGRES_PASSWORD", "ksa")

	cfg := config.DatabaseConfig{
		Host:            host,
		Port:            port,
		Name:            name,
		User:            user,
		Password:        password,
		SSLMode:         "disable",
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 300,
	}

	database, err := db.New(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	machineRepo := db.NewMachineRepository(database)
	nodepoolRepo := db.NewNodepoolRepository(database)

	// Create test nodepool
	nodepool := &models.Nodepool{
		Name:    "test-nodepool-assign-" + randomString(8),
		MinSize: 1,
		MaxSize: 5,
		State:   models.NodepoolStateActive,
	}
	err = nodepoolRepo.Create(ctx, nodepool)
	if err != nil {
		t.Fatalf("Failed to create nodepool: %v", err)
	}

	// Create test machine
	machine := &models.Machine{
		Name:      "test-machine-assign-" + randomString(8),
		IPAddress: "192.168.2." + randomString(3),
		SSHUser:   "root",
		SSHPort:   22,
		State:     models.MachineStateAvailable,
	}
	err = machineRepo.Create(ctx, machine)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Assign machine to nodepool
	err = machineRepo.AssignToNodepool(ctx, machine.ID, nodepool.ID)
	if err != nil {
		t.Fatalf("Failed to assign machine to nodepool: %v", err)
	}

	// Verify assignment
	assigned, err := machineRepo.GetByID(ctx, machine.ID)
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}
	if !assigned.NodepoolID.Valid || assigned.NodepoolID.String != nodepool.ID {
		t.Error("Machine nodepool assignment not set correctly")
	}
	if assigned.State != models.MachineStateInUse {
		t.Errorf("Expected machine state in_use, got %s", assigned.State)
	}

	// Test GetByNodepool
	machines, err := machineRepo.GetByNodepool(ctx, nodepool.ID)
	if err != nil {
		t.Fatalf("Failed to get machines by nodepool: %v", err)
	}
	found := false
	for _, m := range machines {
		if m.ID == machine.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Assigned machine not found in nodepool")
	}

	// Test machine count in nodepool
	count, err := nodepoolRepo.GetMachineCount(ctx, nodepool.ID)
	if err != nil {
		t.Fatalf("Failed to get machine count: %v", err)
	}
	if count < 1 {
		t.Error("Expected at least one machine in nodepool")
	}

	// Unassign machine
	err = machineRepo.UnassignFromNodepool(ctx, machine.ID)
	if err != nil {
		t.Fatalf("Failed to unassign machine: %v", err)
	}

	// Verify unassignment
	unassigned, _ := machineRepo.GetByID(ctx, machine.ID)
	if unassigned.NodepoolID.Valid {
		t.Error("Machine should not have nodepool after unassignment")
	}

	// Cleanup
	machineRepo.Delete(ctx, machine.ID)
	nodepoolRepo.Delete(ctx, nodepool.ID)
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if n, err := parseInt(value); err == nil {
			return n
		}
	}
	return defaultValue
}

func parseInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	now := time.Now().UnixNano()
	for i := 0; i < length; i++ {
		result[i] = charset[(now+int64(i))%int64(len(charset))]
	}
	return string(result)
}
