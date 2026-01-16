/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package models

import (
	"testing"
)

// TestMachineStateIsValid tests machine state validation
func TestMachineStateIsValid(t *testing.T) {
	tests := []struct {
		state    MachineState
		expected bool
	}{
		{MachineStateAvailable, true},
		{MachineStateJoining, true},
		{MachineStateInUse, true},
		{MachineStateDraining, true},
		{MachineStateLeaving, true},
		{MachineStateMaintenance, true},
		{MachineStateFault, true},
		{MachineStateArchived, true},
		{MachineState("invalid"), false},
		{MachineState(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := tt.state.IsValid()
			if result != tt.expected {
				t.Errorf("IsValid() for %q = %v, want %v", tt.state, result, tt.expected)
			}
		})
	}
}

// TestMachineCanTransitionTo tests machine state transitions
func TestMachineCanTransitionTo(t *testing.T) {
	tests := []struct {
		name        string
		current     MachineState
		newState    MachineState
		shouldAllow bool
	}{
		// Available transitions
		{"available to joining", MachineStateAvailable, MachineStateJoining, true},
		{"available to maintenance", MachineStateAvailable, MachineStateMaintenance, true},
		{"available to fault", MachineStateAvailable, MachineStateFault, true},
		{"available to in_use", MachineStateAvailable, MachineStateInUse, false},

		// Joining transitions
		{"joining to in_use", MachineStateJoining, MachineStateInUse, true},
		{"joining to available", MachineStateJoining, MachineStateAvailable, true},
		{"joining to fault", MachineStateJoining, MachineStateFault, true},
		{"joining to leaving", MachineStateJoining, MachineStateLeaving, false},

		// InUse transitions
		{"in_use to draining", MachineStateInUse, MachineStateDraining, true},
		{"in_use to maintenance", MachineStateInUse, MachineStateMaintenance, true},
		{"in_use to fault", MachineStateInUse, MachineStateFault, true},
		{"in_use to available", MachineStateInUse, MachineStateAvailable, false},

		// Draining transitions
		{"draining to leaving", MachineStateDraining, MachineStateLeaving, true},
		{"draining to in_use", MachineStateDraining, MachineStateInUse, true},
		{"draining to available", MachineStateDraining, MachineStateAvailable, true},
		{"draining to fault", MachineStateDraining, MachineStateFault, false},

		// Leaving transitions
		{"leaving to available", MachineStateLeaving, MachineStateAvailable, true},
		{"leaving to archived", MachineStateLeaving, MachineStateArchived, true},
		{"leaving to in_use", MachineStateLeaving, MachineStateInUse, false},

		// Maintenance transitions
		{"maintenance to available", MachineStateMaintenance, MachineStateAvailable, true},
		{"maintenance to fault", MachineStateMaintenance, MachineStateFault, true},
		{"maintenance to in_use", MachineStateMaintenance, MachineStateInUse, false},

		// Fault transitions
		{"fault to available", MachineStateFault, MachineStateAvailable, true},
		{"fault to archived", MachineStateFault, MachineStateArchived, true},
		{"fault to in_use", MachineStateFault, MachineStateInUse, false},

		// Archived is terminal
		{"archived to available", MachineStateArchived, MachineStateAvailable, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := &Machine{State: tt.current}
			result := machine.CanTransitionTo(tt.newState)
			if result != tt.shouldAllow {
				t.Errorf("CanTransitionTo(%s -> %s) = %v, want %v",
					tt.current, tt.newState, result, tt.shouldAllow)
			}
		})
	}
}

// TestNodepoolStateIsValid tests nodepool state validation
func TestNodepoolStateIsValid(t *testing.T) {
	tests := []struct {
		state    NodepoolState
		expected bool
	}{
		{NodepoolStateActive, true},
		{NodepoolStateDisabled, true},
		{NodepoolStateDeleting, true},
		{NodepoolState("invalid"), false},
		{NodepoolState(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := tt.state.IsValid()
			if result != tt.expected {
				t.Errorf("IsValid() for %q = %v, want %v", tt.state, result, tt.expected)
			}
		})
	}
}

// TestDiscoveryStatusIsValid tests discovery status validation
func TestDiscoveryStatusIsValid(t *testing.T) {
	tests := []struct {
		status   DiscoveryStatus
		expected bool
	}{
		{DiscoveryStatusPending, true},
		{DiscoveryStatusSuccess, true},
		{DiscoveryStatusFailed, true},
		{DiscoveryStatusTimeout, true},
		{DiscoveryStatusSkipped, true},
		{DiscoveryStatus("invalid"), false},
		{DiscoveryStatus(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := tt.status.IsValid()
			if result != tt.expected {
				t.Errorf("IsValid() for %q = %v, want %v", tt.status, result, tt.expected)
			}
		})
	}
}

// TestMachineTransitionLoop tests a complete machine lifecycle
func TestMachineTransitionLoop(t *testing.T) {
	machine := &Machine{State: MachineStateAvailable}

	// Normal lifecycle
	lifecycle := []MachineState{
		MachineStateJoining,
		MachineStateInUse,
		MachineStateDraining,
		MachineStateLeaving,
		MachineStateAvailable,
	}

	for i, expectedState := range lifecycle {
		if i > 0 {
			prevState := lifecycle[i-1]
			machine.State = prevState
		}

		if !machine.CanTransitionTo(expectedState) {
			t.Errorf("Cannot transition from %s to %s at step %d",
				machine.State, expectedState, i)
		}
	}
}

// TestMachineFaultRecovery tests fault to recovery transition
func TestMachineFaultRecovery(t *testing.T) {
	machine := &Machine{State: MachineStateFault}

	// Fault machine can go to available (recovery)
	if !machine.CanTransitionTo(MachineStateAvailable) {
		t.Error("Machine in fault state should be able to transition to available")
	}

	// Fault machine can go to archived
	if !machine.CanTransitionTo(MachineStateArchived) {
		t.Error("Machine in fault state should be able to transition to archived")
	}
}

// TestMachineMaintenanceMode tests maintenance mode transitions
func TestMachineMaintenanceMode(t *testing.T) {
	tests := []struct {
		name        string
		current     MachineState
		newState    MachineState
		shouldAllow bool
	}{
		{"available to maintenance", MachineStateAvailable, MachineStateMaintenance, true},
		{"in_use to maintenance", MachineStateInUse, MachineStateMaintenance, true},
		{"joining to maintenance", MachineStateJoining, MachineStateMaintenance, true},
		{"maintenance to available", MachineStateMaintenance, MachineStateAvailable, true},
		{"maintenance to fault", MachineStateMaintenance, MachineStateFault, true},
		{"fault to maintenance", MachineStateFault, MachineStateMaintenance, false},
		{"archived to maintenance", MachineStateArchived, MachineStateMaintenance, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := &Machine{State: tt.current}
			result := machine.CanTransitionTo(tt.newState)
			if result != tt.shouldAllow {
				t.Errorf("CanTransitionTo(%s) from %s = %v, want %v",
					tt.newState, tt.current, result, tt.shouldAllow)
			}
		})
	}
}
