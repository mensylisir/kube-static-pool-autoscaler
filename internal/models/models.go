/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package models

import (
	"database/sql"
	"time"
)

// Machine represents a physical/virtual machine in the pool
type Machine struct {
	ID              string         `db:"id" json:"id"`
	Name            string         `db:"name" json:"name"`
	IPAddress       string         `db:"ip_address" json:"ip_address"`
	SSHUser         string         `db:"ssh_user" json:"ssh_user"`
	SSHPort         int            `db:"ssh_port" json:"ssh_port"`
	SSHKeyPath      string         `db:"ssh_key_path" json:"ssh_key_path"`
	State           MachineState   `db:"state" json:"state"`
	NodepoolID      sql.NullString `db:"nodepool_id" json:"nodepool_id,omitempty"`
	Labels          JSONMap        `db:"labels" json:"labels"`
	Taints          JSONArray      `db:"tains" json:"tains,omitempty"`
	CPU             int            `db:"cpu" json:"cpu"`
	Memory          int64          `db:"memory" json:"memory"` // in bytes
	GPU             sql.NullString `db:"gpu" json:"gpu,omitempty"`
	GPUCount        sql.NullInt64  `db:"gpu_count" json:"gpu_count,omitempty"`
	CreatedAt       time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at" json:"updated_at"`
	LastSeenAt      time.Time      `db:"last_seen_at" json:"last_seen_at"`
	MaintenanceMode bool           `db:"maintenance_mode" json:"maintenance_mode"`
	FaultReason     sql.NullString `db:"fault_reason" json:"fault_reason,omitempty"`
}

// MachineState represents the state of a machine
type MachineState string

const (
	MachineStateAvailable   MachineState = "available"
	MachineStateJoining     MachineState = "joining"
	MachineStateInUse       MachineState = "in_use"
	MachineStateDraining    MachineState = "draining"
	MachineStateLeaving     MachineState = "leaving"
	MachineStateMaintenance MachineState = "maintenance"
	MachineStateFault       MachineState = "fault"
	MachineStateArchived    MachineState = "archived"
)

// IsValid checks if the machine state is valid
func (s MachineState) IsValid() bool {
	switch s {
	case MachineStateAvailable, MachineStateJoining, MachineStateInUse,
		MachineStateDraining, MachineStateLeaving, MachineStateMaintenance,
		MachineStateFault, MachineStateArchived:
		return true
	}
	return false
}

// CanTransitionTo checks if the machine can transition to the given state
func (m *Machine) CanTransitionTo(newState MachineState) bool {
	transitions := map[MachineState][]MachineState{
		MachineStateAvailable: {
			MachineStateJoining, MachineStateMaintenance, MachineStateFault,
		},
		MachineStateJoining: {
			MachineStateInUse, MachineStateAvailable, MachineStateMaintenance, MachineStateFault,
		},
		MachineStateInUse: {
			MachineStateDraining, MachineStateMaintenance, MachineStateFault,
		},
		MachineStateDraining: {
			MachineStateLeaving, MachineStateInUse, MachineStateAvailable,
		},
		MachineStateLeaving: {
			MachineStateAvailable, MachineStateArchived,
		},
		MachineStateMaintenance: {
			MachineStateAvailable, MachineStateFault,
		},
		MachineStateFault: {
			MachineStateAvailable, MachineStateArchived,
		},
		MachineStateArchived: {}, // Terminal state
	}

	allowed, ok := transitions[m.State]
	if !ok {
		return false
	}

	for _, s := range allowed {
		if s == newState {
			return true
		}
	}

	return false
}

// Nodepool represents a pool of machines with similar hardware
type Nodepool struct {
	ID           string         `db:"id" json:"id"`
	Name         string         `db:"name" json:"name"`
	MinSize      int            `db:"min_size" json:"min_size"`
	MaxSize      int            `db:"max_size" json:"max_size"`
	State        NodepoolState  `db:"state" json:"state"`
	Labels       JSONMap        `db:"labels" json:"labels"`
	Taints       JSONArray      `db:"tains" json:"tains,omitempty"`
	CPU          sql.NullInt64  `db:"cpu" json:"cpu,omitempty"`
	Memory       sql.NullInt64  `db:"memory" json:"memory,omitempty"` // in bytes
	GPU          sql.NullString `db:"gpu" json:"gpu,omitempty"`
	GPUCount     sql.NullInt64  `db:"gpu_count" json:"gpu_count,omitempty"`
	InstanceType sql.NullString `db:"instance_type" json:"instance_type,omitempty"`
	CreatedAt    time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time      `db:"updated_at" json:"updated_at"`
}

// NodepoolState represents the state of a nodepool
type NodepoolState string

const (
	NodepoolStateActive   NodepoolState = "active"
	NodepoolStateDisabled NodepoolState = "disabled"
	NodepoolStateDeleting NodepoolState = "deleting"
)

// IsValid checks if the nodepool state is valid
func (s NodepoolState) IsValid() bool {
	switch s {
	case NodepoolStateActive, NodepoolStateDisabled, NodepoolStateDeleting:
		return true
	}
	return false
}

// MachineDiscoveryResult represents the result of a discovery scan
type MachineDiscoveryResult struct {
	ID         string          `db:"id" json:"id"`
	IPAddress  string          `db:"ip_address" json:"ip_address"`
	SSHUser    string          `db:"ssh_user" json:"ssh_user"`
	SSHPort    int             `db:"ssh_port" json:"ssh_port"`
	SSHKeyPath sql.NullString  `db:"ssh_key_path" json:"ssh_key_path,omitempty"`
	CPU        sql.NullInt64   `db:"cpu" json:"cpu,omitempty"`
	Memory     sql.NullInt64   `db:"memory" json:"memory,omitempty"`
	GPU        sql.NullString  `db:"gpu" json:"gpu,omitempty"`
	GPUCount   sql.NullInt64   `db:"gpu_count" json:"gpu_count,omitempty"`
	Hostname   sql.NullString  `db:"hostname" json:"hostname,omitempty"`
	OS         sql.NullString  `db:"os" json:"os,omitempty"`
	SSHStatus  DiscoveryStatus `db:"ssh_status" json:"ssh_status"`
	ErrorMsg   sql.NullString  `db:"error_msg" json:"error_msg,omitempty"`
	ScannedAt  time.Time       `db:"scanned_at" json:"scanned_at"`
}

// DiscoveryStatus represents the status of a discovery scan
type DiscoveryStatus string

const (
	DiscoveryStatusPending DiscoveryStatus = "pending"
	DiscoveryStatusSuccess DiscoveryStatus = "success"
	DiscoveryStatusFailed  DiscoveryStatus = "failed"
	DiscoveryStatusTimeout DiscoveryStatus = "timeout"
	DiscoveryStatusSkipped DiscoveryStatus = "skipped"
)

// IsValid checks if the discovery status is valid
func (s DiscoveryStatus) IsValid() bool {
	switch s {
	case DiscoveryStatusPending, DiscoveryStatusSuccess, DiscoveryStatusFailed,
		DiscoveryStatusTimeout, DiscoveryStatusSkipped:
		return true
	}
	return false
}

// MachineEvent represents an event in a machine's lifecycle
type MachineEvent struct {
	ID        string       `db:"id" json:"id"`
	MachineID string       `db:"machine_id" json:"machine_id"`
	EventType EventType    `db:"event_type" json:"event_type"`
	OldState  MachineState `db:"old_state" json:"old_state"`
	NewState  MachineState `db:"new_state" json:"new_state"`
	Message   string       `db:"message" json:"message"`
	CreatedAt time.Time    `db:"created_at" json:"created_at"`
}

// EventType represents the type of a machine event
type EventType string

const (
	EventTypeStateChange  EventType = "state_change"
	EventTypeAssignment   EventType = "assignment"
	EventTypeUnassignment EventType = "unassignment"
	EventTypeJoin         EventType = "join"
	EventTypeLeave        EventType = "leave"
	EventTypeDrain        EventType = "drain"
	EventTypeReset        EventType = "reset"
	EventTypeFault        EventType = "fault"
	EventTypeRecovery     EventType = "recovery"
	EventTypeMaintenance  EventType = "maintenance"
	EventTypeDiscovery    EventType = "discovery"
)

// JSONMap is a custom type for JSON-encoded maps
type JSONMap map[string]interface{}

// JSONArray is a custom type for JSON-encoded arrays
type JSONArray []interface{}
