/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package grpc

// Generated gRPC message types for External Cloud Provider
// These types match the proto definitions in provider.proto

// GetNodeGroupsRequest is the request for GetNodeGroups
type GetNodeGroupsRequest struct{}

// GetNodeGroupsResponse is the response for GetNodeGroups
type GetNodeGroupsResponse struct {
	NodeGroups []*NodeGroup
}

// GetNodesRequest is the request for GetNodes
type GetNodesRequest struct {
	NodeGroupName string
}

// GetNodesResponse is the response for GetNodes
type GetNodesResponse struct {
	Nodes []*Node
}

// NodeGroupExistsRequest is the request for NodeGroupExists
type NodeGroupExistsRequest struct {
	NodeGroupName string
}

// NodeGroupExistsResponse is the response for NodeGroupExists
type NodeGroupExistsResponse struct {
	Exists bool
}

// GetMachineTypeRequest is the request for GetMachineType
type GetMachineTypeRequest struct {
	NodeName string
}

// GetMachineTypeResponse is the response for GetMachineType
type GetMachineTypeResponse struct {
	MachineType string
}

// TemplateNodeRequest is the request for TemplateNode
type TemplateNodeRequest struct {
	NodeGroupName string
}

// TemplateNodeResponse is the response for TemplateNode
type TemplateNodeResponse struct {
	Node []byte
}

// GetNodeStateRequest is the request for GetNodeState
type GetNodeStateRequest struct {
	NodeName string
}

// GetNodeStateResponse is the response for GetNodeState
type GetNodeStateResponse struct {
	State NodeState
}

// DeleteNodeRequest is the request for DeleteNode
type DeleteNodeRequest struct {
	NodeName string
}

// DeleteNodeResponse is the response for DeleteNode
type DeleteNodeResponse struct{}

// NodeGroup represents a node group
type NodeGroup struct {
	Name       string
	MinSize    int32
	MaxSize    int32
	TargetSize int32
	Labels     map[string]string
	Taints     []string
	DebugInfo  *NodeGroupDebugInfo
}

// NodeGroupDebugInfo contains debug information for a node group
type NodeGroupDebugInfo struct {
	CloudProviderID      string
	NodeGroupCapacity    string
	NodeGroupAllocatable string
	NodeGroupNames       string
}

// Node represents a node
type Node struct {
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Taints      []string
	Spec        *NodeSpec
	Status      *NodeStatus
}

// NodeSpec contains the spec of a node
type NodeSpec struct {
	ProviderID    string
	Unschedulable string
	Taints        []*NodeTaint
}

// NodeTaint represents a taint on a node
type NodeTaint struct {
	Key    string
	Value  string
	Effect string
}

// NodeStatus contains the status of a node
type NodeStatus struct {
	Capacity    *NodeResources
	Allocatable *NodeResources
	Conditions  []*NodeCondition
	Addresses   []*NodeAddress
}

// NodeResources contains resource information
type NodeResources struct {
	Capacity map[string]int64
}

// NodeCondition represents a node condition
type NodeCondition struct {
	Type               string
	Status             string
	Reason             string
	Message            string
	LastTransitionTime int64
}

// NodeAddress represents a node address
type NodeAddress struct {
	Type    string
	Address string
}

// NodeState represents the state of a node
type NodeState int32

const (
	NodeState_UNKNOWN     NodeState = 0
	NodeState_CHECKING    NodeState = 1
	NodeState_READY       NodeState = 2
	NodeState_RUNNING     NodeState = 3
	NodeState_NOT_RUNNING NodeState = 4
	NodeState_STOPPED     NodeState = 5
	NodeState_DELETING    NodeState = 6
)
