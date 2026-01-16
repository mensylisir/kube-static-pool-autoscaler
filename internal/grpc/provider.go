/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package grpc

import (
	"context"
	"fmt"
	"net"
	"time"

	"kube-static-pool-autoscaler/internal/config"
	"kube-static-pool-autoscaler/internal/db"
	"kube-static-pool-autoscaler/internal/models"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/sirupsen/logrus"
)

// Server implements the External Cloud Provider gRPC interface
type Server struct {
	grpc_health_v1.UnimplementedHealthServer
	grpcServer   *grpc.Server
	logger       *logrus.Entry
	db           *db.DB
	machineRepo  *db.MachineRepository
	nodepoolRepo *db.NodepoolRepository
	cfg          config.GRPCConfig
}

// NewServer creates a new gRPC server
func NewServer(cfg config.GRPCConfig, database *db.DB, logger *logrus.Entry) *Server {
	s := &Server{
		logger:       logger,
		db:           database,
		cfg:          cfg,
		machineRepo:  db.NewMachineRepository(database),
		nodepoolRepo: db.NewNodepoolRepository(database),
	}

	var serverOpts []grpc.ServerOption

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		creds, err := credentials.NewServerTLSFromFile(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			logger.Fatalf("Failed to load TLS credentials: %v", err)
		}
		serverOpts = append(serverOpts, grpc.Creds(creds))
	}

	serverOpts = append(serverOpts,
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Second,
			Time:                  1 * time.Minute,
			Timeout:               20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)

	s.grpcServer = grpc.NewServer(serverOpts...)
	grpc_health_v1.RegisterHealthServer(s.grpcServer, s)

	return s
}

// Start begins serving gRPC requests
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	s.logger.Infof("gRPC server listening on %s", addr)
	return s.grpcServer.Serve(lis)
}

// Stop gracefully stops the gRPC server
func (s *Server) Stop(ctx context.Context) {
	s.logger.Info("Stopping gRPC server...")
	s.grpcServer.GracefulStop()
}

// Check implements the health check interface
func (s *Server) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if err := s.db.HealthCheck(ctx); err != nil {
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
		}, nil
	}

	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

// Watch implements the health watch interface
func (s *Server) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	ctx := stream.Context()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := s.db.Ping(); err != nil {
				if err := stream.Send(&grpc_health_v1.HealthCheckResponse{
					Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
				}); err != nil {
					return err
				}
			} else {
				if err := stream.Send(&grpc_health_v1.HealthCheckResponse{
					Status: grpc_health_v1.HealthCheckResponse_SERVING,
				}); err != nil {
					return err
				}
			}
			time.Sleep(5 * time.Second)
		}
	}
}

// GetNodeGroups returns a list of all node groups
func (s *Server) GetNodeGroups(ctx context.Context, req *GetNodeGroupsRequest) (*GetNodeGroupsResponse, error) {
	s.logger.Debug("GetNodeGroups called")

	nodepools, err := s.nodepoolRepo.GetAll(ctx, 1000, 0)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch node groups: %v", err)
	}

	response := &GetNodeGroupsResponse{
		NodeGroups: make([]*NodeGroup, 0, len(nodepools)),
	}

	for _, np := range nodepools {
		if np.State != models.NodepoolStateActive {
			continue
		}

		machineCount, err := s.nodepoolRepo.GetMachineCount(ctx, np.ID)
		if err != nil {
			s.logger.Warnf("Failed to get machine count for nodepool %s: %v", np.ID, err)
			machineCount = 0
		}

		labels := make(map[string]string)
		if np.Labels != nil {
			for k, v := range np.Labels {
				labels[k] = fmt.Sprintf("%v", v)
			}
		}

		taints := []string{}
		if np.Taints != nil {
			for _, t := range np.Taints {
				taints = append(taints, fmt.Sprintf("%v", t))
			}
		}

		nodeGroup := &NodeGroup{
			Name:       np.Name,
			MinSize:    int32(np.MinSize),
			MaxSize:    int32(np.MaxSize),
			TargetSize: int32(machineCount),
			Labels:     labels,
			Taints:     taints,
			DebugInfo:  &NodeGroupDebugInfo{},
		}

		response.NodeGroups = append(response.NodeGroups, nodeGroup)
	}

	return response, nil
}

// GetNodes returns all nodes in a node group
func (s *Server) GetNodes(ctx context.Context, req *GetNodesRequest) (*GetNodesResponse, error) {
	s.logger.Debugf("GetNodes called for group %s", req.NodeGroupName)

	nodepool, err := s.nodepoolRepo.GetByName(ctx, req.NodeGroupName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch nodepool: %v", err)
	}
	if nodepool == nil {
		return nil, status.Errorf(codes.NotFound, "nodepool not found: %s", req.NodeGroupName)
	}

	machines, err := s.machineRepo.GetByNodepool(ctx, nodepool.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch machines: %v", err)
	}

	response := &GetNodesResponse{
		Nodes: make([]*Node, 0, len(machines)),
	}

	for _, m := range machines {
		if m.State != models.MachineStateInUse {
			continue
		}

		node := s.machineToNode(m, nodepool)
		response.Nodes = append(response.Nodes, node)
	}

	return response, nil
}

// NodeGroupExists checks if a node group exists
func (s *Server) NodeGroupExists(ctx context.Context, req *NodeGroupExistsRequest) (*NodeGroupExistsResponse, error) {
	s.logger.Debugf("NodeGroupExists called for group %s", req.NodeGroupName)

	nodepool, err := s.nodepoolRepo.GetByName(ctx, req.NodeGroupName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check nodepool: %v", err)
	}

	return &NodeGroupExistsResponse{
		Exists: nodepool != nil && nodepool.State == models.NodepoolStateActive,
	}, nil
}

// GetMachineType returns the machine type for a node
func (s *Server) GetMachineType(ctx context.Context, req *GetMachineTypeRequest) (*GetMachineTypeResponse, error) {
	s.logger.Debugf("GetMachineType called for node %s", req.NodeName)

	machine, err := s.machineRepo.GetByID(ctx, req.NodeName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch machine: %v", err)
	}
	if machine == nil {
		return nil, status.Errorf(codes.NotFound, "machine not found: %s", req.NodeName)
	}

	machineType := fmt.Sprintf("cpu=%d,memory=%d", machine.CPU, machine.Memory)
	if machine.GPU.Valid {
		machineType = fmt.Sprintf("%s,gpu=%s", machineType, machine.GPU.String)
		if machine.GPUCount.Valid {
			machineType = fmt.Sprintf("%s,gpu-count=%d", machineType, machine.GPUCount.Int64)
		}
	}

	return &GetMachineTypeResponse{
		MachineType: machineType,
	}, nil
}

// TemplateNode returns a template node for a node group
func (s *Server) TemplateNode(ctx context.Context, req *TemplateNodeRequest) (*TemplateNodeResponse, error) {
	s.logger.Debugf("TemplateNode called for group %s", req.NodeGroupName)

	nodepool, err := s.nodepoolRepo.GetByName(ctx, req.NodeGroupName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch nodepool: %v", err)
	}
	if nodepool == nil {
		return nil, status.Errorf(codes.NotFound, "nodepool not found: %s", req.NodeGroupName)
	}

	// Create a template node specification
	template := fmt.Sprintf(`{
  "apiVersion": "v1",
  "kind": "Node",
  "metadata": {
    "labels": {
      "node.kubernetes.io/instance-type": "%s",
      "topology.kubernetes.io/zone": "default"
    },
    "name": "template-%s"
  },
  "spec": {
    "providerID": ":///template"
  },
  "status": {
    "capacity": {
      "cpu": "%d",
      "memory": "%dKi"
    }
  }
}`, nodepool.Name, nodepool.ID, npCPU(nodepool), npMemory(nodepool))

	return &TemplateNodeResponse{
		Node: []byte(template),
	}, nil
}

// GetNodeState returns the state of a node
func (s *Server) GetNodeState(ctx context.Context, req *GetNodeStateRequest) (*GetNodeStateResponse, error) {
	s.logger.Debugf("GetNodeState called for node %s", req.NodeName)

	machine, err := s.machineRepo.GetByID(ctx, req.NodeName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch machine: %v", err)
	}
	if machine == nil {
		return nil, status.Errorf(codes.NotFound, "machine not found: %s", req.NodeName)
	}

	state := s.machineStateToNodeState(machine.State)
	return &GetNodeStateResponse{
		State: state,
	}, nil
}

// DeleteNode removes a node from the node group
func (s *Server) DeleteNode(ctx context.Context, req *DeleteNodeRequest) (*DeleteNodeResponse, error) {
	s.logger.Debugf("DeleteNode called for node %s", req.NodeName)

	machine, err := s.machineRepo.GetByID(ctx, req.NodeName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch machine: %v", err)
	}
	if machine == nil {
		return nil, status.Errorf(codes.NotFound, "machine not found: %s", req.NodeName)
	}

	// Transition to leaving state
	if err := s.machineRepo.UpdateState(ctx, machine.ID, models.MachineStateLeaving); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update machine state: %v", err)
	}

	return &DeleteNodeResponse{}, nil
}

// Helper functions

func (s *Server) machineToNode(m *models.Machine, np *models.Nodepool) *Node {
	labels := make(map[string]string)
	if m.Labels != nil {
		for k, v := range m.Labels {
			labels[k] = fmt.Sprintf("%v", v)
		}
	}
	labels["kubernetes.io/hostname"] = m.IPAddress

	taints := []string{}
	if m.Taints != nil {
		for _, t := range m.Taints {
			taints = append(taints, fmt.Sprintf("%v", t))
		}
	}

	if np != nil {
		labels["node.kubernetes.io/group"] = np.Name
		for k, v := range np.Labels {
			labels[k] = fmt.Sprintf("%v", v)
		}
		taints = append(taints, formatTaints(np.Taints)...)
	}

	return &Node{
		Name:   m.ID,
		Labels: labels,
		Taints: taints,
		Spec:   &NodeSpec{ProviderID: fmt.Sprintf(":/// %s", m.IPAddress)},
	}
}

func (s *Server) machineStateToNodeState(state models.MachineState) NodeState {
	switch state {
	case models.MachineStateJoining:
		return NodeState_CHECKING
	case models.MachineStateInUse:
		return NodeState_READY
	case models.MachineStateDraining, models.MachineStateLeaving:
		return NodeState_DELETING
	case models.MachineStateFault:
		return NodeState_NOT_RUNNING
	default:
		return NodeState_UNKNOWN
	}
}

func npCPU(np *models.Nodepool) int64 {
	if np.CPU.Valid {
		return np.CPU.Int64
	}
	return 4
}

func npMemory(np *models.Nodepool) int64 {
	if np.Memory.Valid {
		return np.Memory.Int64 / 1024 // Convert to Ki
	}
	return 8 * 1024 * 1024 // Default 8Gi = 8Mi Ki
}

func formatTaints(tains models.JSONArray) []string {
	var result []string
	for _, t := range tains {
		result = append(result, fmt.Sprintf("%v", t))
	}
	return result
}

// Unused imports suppression
var _ = credentials.TransportCredentials(nil)
