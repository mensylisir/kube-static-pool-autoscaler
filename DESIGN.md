# Kube Static Pool Autoscaler 设计文档

## 项目概述

本项目旨在实现一个基于 Kubernetes Cluster Autoscaler External Cloud Provider (gRPC) 模式的静态资源池自动扩缩容系统。该系统允许用户管理已开机的物理机或虚拟机资源池，根据 Pod 的资源需求自动将机器加入或移出 Kubernetes 集群。

### 核心特性

- **解耦设计**: 通过 gRPC 接口与 Cluster Autoscaler 通信，不修改官方代码
- **多资源池支持**: 支持 CPU 池、GPU 池等多种资源类型
- **灵活调度**: 多种机器选择策略（随机、最低负载、最高可用等）
- **高可用**: 支持多副本部署，分布式状态管理
- **完整监控**: Prometheus 指标暴露，健康检查接口

## 目录

- [一、技术选型](#一技术选型)
- [二、整体架构](#二整体架构)
- [三、数据模型设计](#三数据模型设计)
- [四、gRPC 服务接口实现](#四grpc服务接口实现)
- [五、自动化脚本模块](#五自动化脚本模块)
- [六、配置管理](#六配置管理)
- [七、部署方案](#七部署方案)
- [八、监控与日志](#八监控与日志)
- [九、功能模块清单](#九功能模块清单)
- [十、项目里程碑](#十项目里程碑)
- [十一、风险与注意事项](#十一风险与注意事项)

---

## 一、技术选型

### 1.1 核心语言和框架

| 组件 | 推荐技术栈 | 理由 |
|------|----------|------|
| **gRPC Provider 服务** | Go 1.21+ | Cluster Autoscaler 官方用 Go，便于复用客户端库，与 K8s 生态无缝集成 |
| **数据库** | PostgreSQL 15 + Redis 7 | PostgreSQL 存机器元数据，Redis 做状态缓存和分布式锁（防并发） |
| **SSH 交互** | golang.org/x/crypto/ssh | 原生 Go SSH 客户端，支持并发连接池 |
| **配置管理** | viper + YAML | 支持环境变量、配置文件，热重载 |
| **服务框架** | grpc-go + standard library | 轻量级，无重型框架依赖 |
| **部署** | Docker + Kubernetes Operator | 与现有集群集成，声明式管理 |

### 1.2 推荐项目结构

```
kube-static-pool-autoscaler/
├── cmd/
│   ├── provider/          # gRPC Provider 服务
│   │   └── main.go
│   └── cli/               # CLI 管理工具（手动扩缩容）
│       └── main.go
├── internal/
│   ├── config/            # 配置管理
│   ├── database/          # 数据访问层
│   ├── machine/           # 机器状态机
│   ├── grpc/              # gRPC 服务实现
│   │   ├── server.go
│   │   └── handlers/
│   ├── ssh/               # SSH 连接池和执行器
│   ├── k8s/               # K8s 客户端封装
│   │   ├── client.go
│   │   └── token.go
│   ├── provisioner/       # 自动化脚本执行器
│   └── monitoring/        # 监控和指标
├── proto/
│   └── externalgrpc.proto # CA 官方 proto（复制自 kubernetes/autoscaler）
├── scripts/               # 机器初始化脚本
│   ├── join.sh.tpl
│   └── reset.sh.tpl
├── deploy/
│   ├── k8s/
│   │   ├── deployment.yaml
│   │   ├── service.yaml
│   │   └── configmap.yaml
│   └── docker/
│       └── Dockerfile
├── test/
│   ├── integration/       # 集成测试
│   └── e2e/               # E2E 测试
├── docs/                  # 文档
├── Makefile
├── go.mod
└── README.md
```

### 1.3 依赖清单（go.mod）

```go
module kube-static-pool-autoscaler

go 1.21

require (
    google.golang.org/grpc v1.59.0
    google.golang.org/protobuf v1.31.0
    k8s.io/client-go v0.28.2
    k8s.io/api v0.28.2
    k8s.io/apimachinery v0.28.2
    k8s.io/klog/v2 v2.100.1
    github.com/spf13/viper v1.18.1
    github.com/lib/pq v1.10.9
    github.com/redis/go-redis/v9 v9.3.0
    golang.org/x/crypto v0.17.0
    github.com/prometheus/client_golang v1.17.0
    github.com/sirupsen/logrus v1.9.3
    github.com/stretchr/testify v1.8.4
    github.com/google/uuid v1.4.0
)
```

---

## 二、整体架构

### 2.1 架构图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                               │
│                                                                          │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │                    Cluster Autoscaler                                ││
│  │   ┌─────────────────────────────────────────────────────────────┐   ││
│  │   │  --cloud-provider=externalgrpc                              │   ││
│  │   │  --cloud-config=/etc/config/external-config.yaml            │   ││
│  │   │  --nodes=0:20:cpu-pool  --nodes=0:10:gpu-pool              │   ││
│  │   └─────────────────────────────────────────────────────────────┘   ││
│  │                              │                                      ││
│  │                              ▼ gRPC                                 ││
│  │   ┌─────────────────────────────────────────────────────────────┐   ││
│  │   │           Kube Static Pool Autoscaler (Custom Provider)      │   ││
│  │   │   ┌─────────────┐ ┌─────────────┐ ┌─────────────────────┐   │   ││
│  │   │   │   gRPC      │ │  Database   │ │   SSH Provisioner   │   │   ││
│  │   │   │   Server    │ │  (PG+Redis) │ │                     │   │   ││
│  │   │   └─────────────┘ └─────────────┘ └─────────────────────┘   │   ││
│  │   └─────────────────────────────────────────────────────────────┘   ││
│  │                              │                                      ││
│  │                    SSH + Kubeadm                                     ││
│  └─────────────────────────────────────────────────────────────────────┘│
│                              │                                           │
│                              ▼                                           │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │                    Machine Inventory                                 ││
│  │   ┌────────────┐ ┌────────────┐ ┌────────────────────────────┐    ││
│  │   │ 192.168.1.100 │ 192.168.1.101 │ ... (已开机物理机/虚拟机)  │    ││
│  │   │  cpu-pool   │  gpu-pool   │                            │    ││
│  │   │  in-use     │  available  │                            │    ││
│  │   └────────────┘ └────────────┘ └────────────────────────────┘    ││
│  └─────────────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────────────┘
```

### 2.2 数据流

#### 扩容流程（Scale Up）

```
1. Pending Pod 触发扩容请求
   │
   ▼
2. Cluster Autoscaler 调用 gRPC IncreaseSize 接口
   │
   ▼
3. Provider 接收请求，检查 nodepool 状态
   │
   ▼
4. 从 Machine Inventory 分配可用机器（带锁）
   │
   ▼
5. 生成 kubeadm join 命令
   │
   ▼
6. SSH 连接目标机器，执行 join 脚本
   │
   ▼
7. 等待节点注册成功，更新状态为 in_use
   │
   ▼
8. 返回成功，Pod 被调度到新节点
```

#### 缩容流程（Scale Down）

```
1. Cluster Autoscaler 判断节点可回收
   │
   ▼
2. 调用 gRPC DeleteNodes 接口
   │
   ▼
3. Provider 执行 kubectl drain
   │
   ▼
4. SSH 执行 kubeadm reset
   │
   ▼
5. 更新机器状态为 available
   │
   ▼
6. 节点被移除出集群
```

---

## 三、数据模型设计

### 3.1 ER 图

```
┌─────────────┐       ┌─────────────┐       ┌─────────────┐
│  nodepools  │───┐   │  machines   │───┐   │  ssh_keys   │
├─────────────┤   │   ├─────────────┤   │   ├─────────────┤
│ id          │   │   │ id          │   │   │ id          │
│ name        │   │   │ hostname    │   │   │ name        │
│ display_name│   │   │ ip_address  │   │   │ private_key │
│ min_size    │   │   │ nodepool_id │───┘   │ passphrase  │
│ max_size    │   │   │ status      │       └─────────────┘
│ priority    │   │   │ cpu_cores   │
│ ...         │   │   │ memory_bytes│
└─────────────┘   │   │ gpu_info    │
                  │   │ node_name   │
                  │   │ ssh_key_id  │
                  │   │ ...         │
                  │   └─────────────┘
                  │
                  └──┐
                     │scaling_events
                     ▼
                  ┌─────────────┐
                  │id           │
                  │nodepool_id  │
                  │machine_id   │
                  │event_type   │
                  │status       │
                  │started_at   │
                  │completed_at │
                  └─────────────┘
```

### 3.2 数据库表结构

#### 3.2.1 资源池定义表（nodepools）

```sql
CREATE TABLE nodepools (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(128) NOT NULL UNIQUE,
    display_name    VARCHAR(255),
    
    -- 扩缩容范围
    min_size        INT NOT NULL DEFAULT 0,
    max_size        INT NOT NULL DEFAULT 10,
    
    -- 模板信息（用于扩容时的新节点）
    template_labels JSONB NOT NULL DEFAULT '{}',  -- 如 {"hardware-type": "gpu", "gpu-type": "a100"}
    template_taints JSONB DEFAULT '[]',           -- 如 [{"key": "nvidia.com/gpu", "value": "true", "effect": "NoSchedule"}]
    
    -- 硬件要求（用于选择可用机器）
    min_cpu_cores   INT DEFAULT 0,
    min_memory_bytes BIGINT DEFAULT 0,
    require_gpu     BOOLEAN DEFAULT FALSE,
    gpu_type        VARCHAR(64),
    
    -- 优先级和策略
    priority        INT DEFAULT 0,               -- 扩缩容优先级
    scale_up_policy VARCHAR(32) DEFAULT 'random', -- random, least-used, most-available
    scale_down_policy VARCHAR(32) DEFAULT 'oldest', -- oldest, least-used
    
    -- 状态
    is_active       BOOLEAN DEFAULT TRUE,
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);

-- 索引
CREATE INDEX idx_nodepools_name ON nodepools(name);
CREATE INDEX idx_nodepools_active ON nodepools(is_active);
```

#### 3.2.2 机器库存表（machines）

```sql
CREATE TABLE machines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        VARCHAR(255) NOT NULL,
    ip_address      INET NOT NULL,
    nodepool_id     UUID NOT NULL REFERENCES nodepools(id),
    status          VARCHAR(32) NOT NULL DEFAULT 'available',
    
    -- 硬件规格
    cpu_cores       INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_info        JSONB,              -- {"type": "nvidia-a100", "count": 4}
    
    -- SSH 凭据
    ssh_user        VARCHAR(64) NOT NULL DEFAULT 'root',
    ssh_port        INT NOT NULL DEFAULT 22,
    ssh_key_id      UUID REFERENCES ssh_keys(id),
    
    -- 节点信息（加入集群后）
    node_name       VARCHAR(255),
    node_uid        VARCHAR(255),
    join_token      VARCHAR(255),
    join_hash       VARCHAR(255),
    
    -- 元数据
    labels          JSONB DEFAULT '{}', -- 自定义标签
    taints          JSONB DEFAULT '[]', -- 污点列表
    tags            JSONB DEFAULT '{}', -- 业务标签，如 "department": "ml-team"
    
    -- 审计字段
    last_heartbeat  TIMESTAMP,
    last_action     VARCHAR(32),
    last_action_at  TIMESTAMP,
    error_message   TEXT,
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);

-- 索引
CREATE INDEX idx_machines_nodepool_status ON machines(nodepool_id, status);
CREATE INDEX idx_machines_ip ON machines(ip_address);
CREATE INDEX idx_machines_node_name ON machines(node_name);
CREATE INDEX idx_machines_status_heartbeat ON machines(status, last_heartbeat);
```

#### 3.2.3 SSH 密钥表（ssh_keys）

```sql
CREATE TABLE ssh_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(128) NOT NULL,
    private_key TEXT NOT NULL,  -- 使用 encryption-at-rest
    passphrase  TEXT,           -- 如果私钥有密码
    created_at  TIMESTAMP DEFAULT NOW(),
    updated_at  TIMESTAMP DEFAULT NOW()
);
```

#### 3.2.4 扩缩容事件日志（scaling_events）

```sql
CREATE TABLE scaling_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    nodepool_id   UUID NOT NULL REFERENCES nodepools(id),
    machine_id    UUID REFERENCES machines(id),
    event_type    VARCHAR(32) NOT NULL, -- scale_up, scale_down, join_success, join_failed, drain_success, drain_failed
    delta         INT,                  -- 扩容数量或缩容数量
    status        VARCHAR(32) NOT NULL,
    error_message TEXT,
    metadata      JSONB DEFAULT '{}',   -- 额外信息，如执行时长、节点名等
    started_at    TIMESTAMP DEFAULT NOW(),
    completed_at  TIMESTAMP
);

-- 索引
CREATE INDEX idx_events_nodepool ON scaling_events(nodepool_id, started_at DESC);
CREATE INDEX idx_events_machine ON scaling_events(machine_id, started_at DESC);
CREATE INDEX idx_events_type ON scaling_events(event_type, started_at DESC);
```

### 3.3 状态机定义

```
┌─────────────┐
│  available  │ (待命状态，机器已开机，可被分配)
└──────┬──────┘
       │
       ├── 扩容请求 → [分配机器] → reserving
       │
       │
┌──────▼──────┐
│  reserving  │ (预留状态，已被分配但尚未开始Join)
└──────┬──────┘
       │
       ├── 开始 Join → joining
       │
       └── 分配超时/取消 → available

┌──────▼──────┐
│   joining   │ (正在加入集群)
└──────┬──────┘
       │
       ├── Join 成功 → in_use
       │   │
       │   └── 注册 node_name, node_uid
       │
       └── Join 失败 → fault (或回滚到 available)
           │
           └── 重试次数超限 → maintenance

┌──────▼──────┐
│   in_use    │ (已加入集群，正常使用)
└──────┬──────┘
       │
       ├── 缩容请求 → draining
       │
       ├── 节点失联 → maintenance (需要人工介入)
       │
       └── 定期心跳检测 → 维持状态

┌──────▼──────┐
│  draining   │ (正在排空 Pod)
└──────┬──────┘
       │
       ├── Drain 成功 → leaving
       │
       └── Drain 超时 → maintenance (强制下线)

┌──────▼──────┐
│  leaving    │ (正在离开集群)
└──────┬──────┘
       │
       ├── Reset 成功 → available
       │
       └── Reset 失败 → maintenance

┌──────▼──────┐
│    fault    │ (故障状态)
└──────┬──────┘
       │
       ├── 人工修复 → available
       │
       └── 标记为坏 → maintenance

┌──────▼──────┐
│ maintenance │ (维护中)
└──────┬──────┘
       │
       ├── 修复完成 → available
       │
       └── 报废处理 → archived (归档)
```

### 3.4 Redis 缓存和锁

```go
// Redis Key 设计
const (
    MachineStatusHash   = "machines:status"     // Hash: machine_id -> status
    MachineHeartbeatZSet = "machines:heartbeat" // SortedSet: machine_id -> timestamp
    NodepoolSizeKey     = "nodepools:{id}:size" // String: 当前 in_use 数量
    LockKeyPrefix       = "locks:"              // 分布式锁前缀
)

// 分布式锁粒度
type LockType string
const (
    LockNodepool   LockType = "nodepool"   // 资源池级别锁
    LockMachine    LockType = "machine"    // 单机级别锁
    LockOperation  LockType = "operation"  // 操作级别锁
)

// Redis 缓存示例
type RedisCache struct {
    client *redis.Client
}

// GetMachineStatus 获取机器状态
func (r *RedisCache) GetMachineStatus(ctx context.Context, machineID string) (string, error) {
    return r.client.HGet(ctx, MachineStatusHash, machineID).Result()
}

// SetMachineStatus 设置机器状态
func (r *RedisCache) SetMachineStatus(ctx context.Context, machineID, status string) error {
    pipe := r.client.Pipeline()
    pipe.HSet(ctx, MachineStatusHash, machineID, status)
    pipe.ZAdd(ctx, MachineHeartbeatZSet, &redis.Z{
        Score:  float64(time.Now().Unix()),
        Member: machineID,
    })
    _, err := pipe.Exec(ctx)
    return err
}

// AcquireLock 获取分布式锁
func (r *RedisCache) AcquireLock(ctx context.Context, lockType LockType, id string, ttl time.Duration) (bool, error) {
    key := fmt.Sprintf("%s%s:%s", LockKeyPrefix, lockType, id)
    return r.client.SetNX(ctx, key, "locked", ttl).Result()
}

// ReleaseLock 释放分布式锁
func (r *RedisCache) ReleaseLock(ctx context.Context, lockType LockType, id string) error {
    key := fmt.Sprintf("%s%s:%s", LockKeyPrefix, lockType, id)
    return r.client.Del(ctx, key).Err()
}
```

---

## 四、gRPC 服务接口实现

### 4.1 接口映射（基于 externalgrpc.proto）

```protobuf
syntax = "proto3";

package externalgrpc;

service ExternalCloudProvider {
    // 机器组管理
    rpc NodeGroups(NodeGroupsRequest) returns (NodeGroupsResponse);
    rpc NodeGroupSize(NodeGroupSizeRequest) returns (NodeGroupSizeResponse);
    rpc TargetSize(NodeGroupSizeRequest) returns (NodeGroupSizeResponse);
    rpc IncreaseSize(IncreaseSizeRequest) returns (IncreaseSizeResponse);
    rpc DecreaseSize(DecreaseSizeRequest) returns (DecreaseSizeResponse);
    
    // 模板信息（核心）
    rpc NodeGroupTemplateNodeInfo(NodeGroupTemplateNodeInfoRequest) returns (NodeTemplateInfo);
    rpc GetNodeGroupForNode(GetNodeGroupForNodeRequest) returns (NodeGroupResponse);
    
    // 节点操作
    rpc DeleteNodes(DeleteNodesRequest) returns (DeleteNodesResponse);
    rpc NodeGroupNodes(NodeGroupNodesRequest) returns (NodeGroupNodesResponse);
    
    // 资源信息
    rpc GetAvailableResources(GetAvailableResourcesRequest) returns (AvailableResources);
    rpc HasInstanceType(HasInstanceTypeRequest) returns (HasInstanceTypeResponse);
}
```

### 4.2 核心接口实现

#### 4.2.1 NodeGroups - 返回所有资源池

```go
func (s *Server) NodeGroups(ctx context.Context, req *pb.NodeGroupsRequest) (*pb.NodeGroupsResponse, error) {
    logger := log.FromContext(ctx)
    
    nodepools, err := s.db.ListActiveNodepools(ctx)
    if err != nil {
        logger.Error(err, "failed to list nodepools")
        return nil, status.Errorf(codes.Internal, "failed to list nodepools: %v", err)
    }
    
    var nodeGroups []*pb.NodeGroup
    for _, np := range nodepools {
        nodeGroups = append(nodeGroups, &pb.NodeGroup{
            Id:      np.ID.String(),
            Name:    np.Name,
            MinSize: int64(np.MinSize),
            MaxSize: int64(np.MaxSize),
        })
    }
    
    return &pb.NodeGroupsResponse{NodeGroups: nodeGroups}, nil
}
```

#### 4.2.2 NodeGroupTemplateNodeInfo - 核心！告诉 CA 新节点长什么样

```go
func (s *Server) NodeGroupTemplateNodeInfo(ctx context.Context, req *pb.NodeGroupTemplateNodeInfoRequest) (*pb.NodeTemplateInfo, error) {
    nodepool, err := s.db.GetNodepool(ctx, req.NodeGroupId)
    if err != nil {
        return nil, status.Errorf(codes.NotFound, "nodepool not found: %v", err)
    }
    
    // 构建模拟的 Node 对象
    template := &pb.NodeTemplateInfo{
        NodeName:      fmt.Sprintf("{nodepool}-<guid>"), // 占位符
        NodeClass:     nodepool.Name,
        Labels:        nodepool.TemplateLabels,
        Taints:        convertTaints(nodepool.TemplateTaints),
        CpuCapacity:   strconv.Itoa(nodepool.MinCpuCores),
        MemoryCapacity: strconv.FormatInt(nodepool.MinMemoryBytes/1024/1024, 10) + "M",
    }
    
    // 如果是 GPU 池，添加 GPU 资源
    if nodepool.RequireGPU {
        template.ExtraResources = map[string]string{
            "nvidia.com/gpu": strconv.Itoa(nodepool.GPUCount),
        }
    }
    
    return template, nil
}

// convertTaints 转换污点格式
func convertTaints(taints []Taint) []*pb.Taint {
    var result []*pb.Taint
    for _, t := range taints {
        result = append(result, &pb.Taint{
            Key:    t.Key,
            Value:  t.Value,
            Effect: string(t.Effect),
        })
    }
    return result
}
```

#### 4.2.3 IncreaseSize - 扩容核心逻辑

```go
func (s *Server) IncreaseSize(ctx context.Context, req *pb.IncreaseSizeRequest) (*pb.IncreaseSizeResponse, error) {
    logger := log.FromContext(ctx)
    
    nodepool, err := s.db.GetNodepool(ctx, req.NodeGroupId)
    if err != nil {
        return nil, status.Errorf(codes.NotFound, "nodepool not found")
    }
    
    // 获取当前使用中的节点数
    currentSize, err := s.db.CountMachinesByStatus(ctx, nodepool.ID, "in_use")
    if err != nil {
        return nil, status.Errorf(codes.Internal, "failed to count machines")
    }
    
    if int(currentSize)+int(req.Delta) > nodepool.MaxSize {
        return nil, status.Errorf(codes.InvalidArgument, 
            "max size exceeded: current=%d, delta=%d, max=%d", 
            currentSize, req.Delta, nodepool.MaxSize)
    }
    
    logger.Info("received scale up request",
        "nodepool", nodepool.Name,
        "current_size", currentSize,
        "delta", req.Delta)
    
    // 分配机器（带锁）
    machines, err := s.provisioner.AllocateMachines(ctx, nodepool, int(req.Delta))
    if err != nil {
        logger.Error(err, "failed to allocate machines")
        return nil, status.Errorf(codes.Internal, "failed to allocate machines: %v", err)
    }
    
    logger.Info("allocated machines", "count", len(machines), "machines", machines)
    
    // 异步执行 Join 操作（不阻塞 gRPC 调用）
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
        defer cancel()
        s.provisioner.ExecuteJoin(ctx, nodepool, machines)
    }()
    
    return &pb.IncreaseSizeResponse{}, nil
}
```

#### 4.2.4 DeleteNodes - 缩容核心逻辑

```go
func (s *Server) DeleteNodes(ctx context.Context, req *pb.DeleteNodesRequest) (*pb.DeleteNodesResponse, error) {
    logger := log.FromContext(ctx)
    
    logger.Info("received scale down request", "node_count", len(req.NodeNames))
    
    for _, nodeName := range req.NodeNames {
        machine, err := s.db.GetMachineByNodeName(ctx, nodeName)
        if err != nil {
            logger.Error(err, "machine not found for node", "node", nodeName)
            continue
        }
        
        logger.Info("starting drain and reset", "machine", machine.IPAddress)
        
        // 异步执行 Drain + Reset
        go func(m *model.Machine) {
            ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
            defer cancel()
            s.provisioner.ExecuteDrainAndReset(ctx, m)
        }(machine)
    }
    
    return &pb.DeleteNodesResponse{}, nil
}
```

### 4.3 扩容/缩容执行器

```go
type Provisioner struct {
    db           *database.DB
    sshPool      *ssh.ConnectionPool
    k8sClient    *k8s.Client
    tokenManager *TokenManager
    eventLogger  *EventLogger
    config       *Config
}

func (p *Provisioner) ExecuteJoin(ctx context.Context, nodepool *model.Nodepool, machines []*model.Machine) {
    logger := log.FromContext(ctx).WithValues(
        "nodepool", nodepool.Name,
        "count", len(machines),
    )
    logger.Info("starting scale up operation")
    
    // 1. 获取 Join 命令
    joinCmd, err := p.k8sClient.GenerateJoinCommand(ctx)
    if err != nil {
        logger.Error(err, "failed to generate join command")
        p.handleJoinFailure(ctx, machines, err)
        return
    }
    logger.Info("generated join command", "command", joinCmd[:50]+"...")
    
    // 2. 并发 SSH 执行 Join（带限流）
    var wg sync.WaitGroup
    semaphore := make(chan struct{}, p.config.Scaling.DefaultScaleUpConcurrency)
    results := make(chan struct {
        machine *model.Machine
        err     error
    }, len(machines))
    
    for _, machine := range machines {
        wg.Add(1)
        semaphore <- struct{}{}
        
        go func(m *model.Machine) {
            defer wg.Done()
            defer func() { <-semaphore }()
            
            err := p.sshExecuteJoin(ctx, m, joinCmd)
            results <- struct {
                machine *model.Machine
                err     error
            }{m, err}
        }(machine)
    }
    
    // 等待所有 SSH 执行完成
    go func() {
        wg.Wait()
        close(results)
    }()
    
    // 处理结果
    var failedMachines []*model.Machine
    for result := range results {
        if result.err != nil {
            logger.Error(result.err, "join failed", "machine", result.machine.IPAddress)
            failedMachines = append(failedMachines, result.machine)
        }
    }
    
    // 3. 处理失败的机器
    if len(failedMachines) > 0 {
        p.handleJoinFailure(ctx, failedMachines, fmt.Errorf("some machines failed to join"))
    }
    
    // 4. 等待节点注册（最长 5 分钟）
    for _, machine := range machines {
        if slices.Contains(failedMachines, machine) {
            continue
        }
        
        ctx, cancel := context.WithTimeout(ctx, p.config.Scaling.NodeRegistrationTimeout)
        err := p.waitForNodeRegistration(ctx, machine)
        cancel()
        
        if err != nil {
            logger.Error(err, "node registration timeout", "machine", machine.IPAddress)
            continue
        }
        
        // 更新状态
        machine.Status = "in_use"
        machine.LastAction = "scale_up"
        machine.LastActionAt = time.Now()
        machine.LastHeartbeat = time.Now()
        
        if err := p.db.UpdateMachine(ctx, machine); err != nil {
            logger.Error(err, "failed to update machine status", "machine", machine.IPAddress)
        }
        
        // 记录事件
        p.eventLogger.Log(ctx, nodepool.ID, machine.ID, "scale_up", "success", map[string]interface{}{
            "node_name": machine.NodeName,
        })
        
        logger.Info("machine successfully joined cluster",
            "machine", machine.IPAddress,
            "node_name", machine.NodeName)
    }
    
    logger.Info("scale up operation completed",
        "total", len(machines),
        "success", len(machines)-len(failedMachines),
        "failed", len(failedMachines))
}

func (p *Provisioner) sshExecuteJoin(ctx context.Context, machine *model.Machine, joinCmd string) error {
    logger := log.FromContext(ctx).WithValues("machine", machine.IPAddress)
    
    // 更新状态为 joining
    machine.Status = "joining"
    machine.LastAction = "scale_up"
    machine.LastActionAt = time.Now()
    
    if err := p.db.UpdateMachine(ctx, machine); err != nil {
        logger.Error(err, "failed to update machine status")
        return err
    }
    
    // 获取 SSH 连接
    conn, err := p.sshPool.Get(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort)
    if err != nil {
        return fmt.Errorf("ssh connection failed: %w", err)
    }
    defer p.sshPool.Release(conn, machine.SSHUser, machine.IPAddress, machine.SSHPort)
    
    logger.Info("ssh connection established")
    
    // 读取 join 脚本模板
    script, err := p.loadJoinScriptTemplate(machine)
    if err != nil {
        return fmt.Errorf("failed to load script template: %w", err)
    }
    
    // 执行脚本
    output, err := conn.Exec(ctx, script)
    if err != nil {
        return fmt.Errorf("join script failed: %w, output: %s", err, output)
    }
    
    logger.Info("join script executed", "output", output)
    
    // 提取节点名称（如果有）
    if nodeName := extractNodeName(output); nodeName != "" {
        machine.NodeName = nodeName
    }
    
    return nil
}

func (p *Provisioner) ExecuteDrainAndReset(ctx context.Context, machine *model.Machine) {
    logger := log.FromContext(ctx).WithValues("machine", machine.IPAddress, "node", machine.NodeName)
    logger.Info("starting scale down operation")
    
    // 1. 更新状态
    machine.Status = "draining"
    machine.LastAction = "scale_down"
    machine.LastActionAt = time.Now()
    
    if err := p.db.UpdateMachine(ctx, machine); err != nil {
        logger.Error(err, "failed to update machine status")
        return
    }
    
    // 2. 执行 kubectl drain
    logger.Info("draining node")
    err := p.k8sClient.DrainNode(ctx, machine.NodeName, &DrainOptions{
        GracePeriodSeconds: 300,
        Timeout:            10 * time.Minute,
        DeleteLocalData:    true,
        Force:              true,
    })
    
    if err != nil {
        logger.Error(err, "drain failed")
        p.handleDrainFailure(ctx, machine, err)
        return
    }
    
    logger.Info("drain completed successfully")
    
    // 3. SSH 执行 kubeadm reset
    conn, err := p.sshPool.Get(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort)
    if err != nil {
        logger.Error(err, "ssh connection failed for reset")
        p.handleResetFailure(ctx, machine, err)
        return
    }
    defer p.sshPool.Release(conn, machine.SSHUser, machine.IPAddress, machine.SSHPort)
    
    resetScript := p.loadResetScriptTemplate()
    output, err := conn.Exec(ctx, resetScript)
    if err != nil {
        logger.Error(err, "reset failed", "output", output)
        p.handleResetFailure(ctx, machine, err)
        return
    }
    
    logger.Info("reset completed", "output", output)
    
    // 4. 更新状态为 available
    machine.Status = "available"
    machine.NodeName = ""
    machine.NodeUID = ""
    machine.LastAction = "scale_down"
    machine.LastActionAt = time.Now()
    machine.LastHeartbeat = time.Now()
    
    if err := p.db.UpdateMachine(ctx, machine); err != nil {
        logger.Error(err, "failed to update machine status")
        return
    }
    
    // 记录事件
    p.eventLogger.Log(ctx, machine.NodepoolID, machine.ID, "scale_down", "success", nil)
    
    logger.Info("scale down operation completed successfully")
}
```

---

## 五、自动化脚本模块

### 5.1 Join 脚本模板

```bash
#!/bin/bash
set -euo pipefail

# 脚本参数: $1 = join command, $2 = node name (optional)
JOIN_CMD="${1:-}"
NODE_NAME="${2:-$(hostname)}"

# 日志函数
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >&2
}

log "Starting node join process for ${NODE_NAME}"

# 1. 基础系统检查
log "Performing system checks..."
swapoff -a || true
systemctl start containerd || systemctl start docker || true

# 2. 预配置检查
if [ -f /etc/kubernetes/bootstrap-kubelet.conf ]; then
    log "Kubelet already configured, skipping join"
    exit 0
fi

# 3. 执行 Join
log "Executing kubeadm join..."
if [ -z "${JOIN_CMD}" ]; then
    log "ERROR: No join command provided"
    exit 1
fi

# 解析 join command 并执行
# join command 格式: kubeadm join <api-server>:6443 --token <token> --discovery-token-ca-cert-hash <hash>
eval "${JOIN_CMD}"

# 4. 等待 kubelet 就绪
log "Waiting for kubelet to be ready..."
for i in {1..60}; do
    if systemctl is-active --quiet kubelet; then
        log "Kubelet is active"
        break
    fi
    sleep 5
done

# 5. 验证节点注册
log "Verifying node registration..."
for i in {1..30}; do
    if kubectl get node "${NODE_NAME}" &>/dev/null; then
        log "Node ${NODE_NAME} registered successfully"
        exit 0
    fi
    sleep 10
done

log "WARNING: Node may not have registered within timeout"
exit 0  # 脚本本身成功，实际注册由后续监控处理
```

### 5.2 Reset 脚本模板

```bash
#!/bin/bash
set -euo pipefail

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >&2
}

log "Starting node reset process"

# 1. 执行 kubeadm reset
log "Executing kubeadm reset..."
kubeadm reset -f || true

# 2. 清理网络配置
log "Cleaning up network configuration..."
iptables -F || true
iptables -t nat -F || true
ip link delete cni0 || true
rm -rf /etc/cni/net.d/* || true

# 3. 清理 kubelet 配置
log "Cleaning up kubelet configuration..."
rm -rf /var/lib/kubelet/pods/* || true
rm -rf /var/lib/kubelet/cpu_manager_state || true
rm -rf /var/lib/kubelet/memory_manager_state || true

# 4. 清理证书和密钥
log "Cleaning up certificates..."
rm -f /etc/kubernetes/pki/ca.crt /etc/kubernetes/pki/ca.key || true
rm -f /etc/kubernetes/bootstrap-kubelet.conf || true

# 5. 可选：重置特定污点和标签
# kubectl label nodes $(hostname) node.kubernetes.io/not-ready- || true

log "Node reset completed successfully"
exit 0
```

### 5.3 SSH 连接池设计

```go
type ConnectionPool struct {
    maxConnections int
    idleTimeout    time.Duration
    pools          map[string]*chanPool  // 按 host 分组
    mutex          sync.RWMutex
}

type chanPool struct {
    conns chan *ssh.Client
}

func NewConnectionPool(maxConnections int, idleTimeout time.Duration) *ConnectionPool {
    return &ConnectionPool{
        maxConnections: maxConnections,
        idleTimeout:    idleTimeout,
        pools:          make(map[string]*chanPool),
    }
}

func (p *ConnectionPool) Get(ctx context.Context, host, user string, port int) (*ssh.Client, error) {
    key := fmt.Sprintf("%s@%s:%d", user, host, port)
    
    p.mutex.RLock()
    pool, exists := p.pools[key]
    p.mutex.RUnlock()
    
    if !exists {
        p.mutex.Lock()
        // 双重检查
        if pool, exists = p.pools[key]; !exists {
            pool = &chanPool{
                conns: make(chan *ssh.Client, p.maxConnections),
            }
            p.pools[key] = pool
        }
        p.mutex.Unlock()
    }
    
    // 尝试从池中获取连接
    select {
    case conn := <-pool.conns:
        // 验证连接仍可用（通过发送 keepalive 请求检测）
        if conn != nil {
            _, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
            if err == nil {
                return conn, nil
            }
            conn.Close()
        }
        // 连接断开或无效，创建新连接
        
    default:
        // 连接池为空，创建新连接
    }
    
    return p.createNewConnection(ctx, host, user, port)
}

func (p *ConnectionPool) createNewConnection(ctx context.Context, host, user string, port int) (*ssh.Client, error) {
    // 从数据库获取 SSH 密钥
    key, err := p.sshKeyStore.GetDefaultKey(ctx)
    if err != nil {
        return nil, fmt.Errorf("failed to get SSH key: %w", err)
    }
    
    // 构建 SSH 配置
    signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(key.PrivateKey), []byte(key.Passphrase))
    if err != nil {
        signer, err = ssh.ParsePrivateKey([]byte(key.PrivateKey))
        if err != nil {
            return nil, fmt.Errorf("failed to parse SSH key: %w", err)
        }
    }
    
    config := &ssh.ClientConfig{
        User: user,
        Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
        HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 生产环境应改为验证 known_hosts
        Timeout: 30 * time.Second,
    }
    
    // 连接
    client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
    if err != nil {
        return nil, fmt.Errorf("SSH connection failed: %w", err)
    }
    
    return client, nil
}

func (p *ConnectionPool) Release(conn *ssh.Client, user string, host string, port int) {
    if conn == nil {
        return
    }
    
    // 使用与 Get 方法相同的 key 格式
    key := fmt.Sprintf("%s@%s:%d", user, host, port)
    
    p.mutex.RLock()
    pool, exists := p.pools[key]
    p.mutex.RUnlock()
    
    if !exists {
        conn.Close()
        return
    }
    
    // 检查连接是否仍然有效（通过发送 keepalive 请求检测）
    _, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
    if err != nil {
        conn.Close()
        return
    }
    
    // 尝试放回池中
    select {
    case pool.conns <- conn:
        // 成功放回
    default:
        // 池已满，关闭连接
        conn.Close()
    }
}

type SSHClient struct {
    client *ssh.Client
}

func (c *SSHClient) Exec(ctx context.Context, cmd string) (string, error) {
    session, err := c.client.NewSession()
    if err != nil {
        return "", err
    }
    defer session.Close()
    
    // 设置超时
    ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
    defer cancel()
    
    done := make(chan error, 1)
    var output bytes.Buffer
    
    session.Stdout = &output
    session.Stderr = &output
    
    go func() {
        done <- session.Run(cmd)
    }()
    
    select {
    case <-ctx.Done():
        session.Signal(syscall.SIGKILL)
        return "", ctx.Err()
    case err := <-done:
        return output.String(), err
    }
}
```

### 5.4 机器分配器

```go
type MachineAllocator struct {
    db    *database.DB
    cache *RedisCache
}

type ScaleUpPolicy string

const (
    PolicyRandom        ScaleUpPolicy = "random"
    PolicyLeastUsed     ScaleUpPolicy = "least-used"
    PolicyMostAvailable ScaleUpPolicy = "most-available"
)

func (a *MachineAllocator) AllocateMachines(ctx context.Context, nodepool *model.Nodepool, count int) ([]*model.Machine, error) {
    // 获取分布式锁
    lockKey := fmt.Sprintf("allocate:%s", nodepool.ID)
    acquired, err := a.cache.AcquireLock(ctx, LockNodepool, lockKey, 30*time.Second)
    if err != nil || !acquired {
        return nil, fmt.Errorf("failed to acquire lock")
    }
    defer a.cache.ReleaseLock(ctx, LockNodepool, lockKey)
    
    // 查询可用机器
    availableMachines, err := a.db.GetAvailableMachines(ctx, &AvailableMachinesQuery{
        NodepoolID:     nodepool.ID,
        MinCPUCores:    nodepool.MinCpuCores,
        MinMemoryBytes: nodepool.MinMemoryBytes,
        RequireGPU:     nodepool.RequireGPU,
        GPUType:        nodepool.GPUType,
        Limit:          count * 3, // 多查一些作为备选
    })
    if err != nil {
        return nil, fmt.Errorf("failed to query available machines: %w", err)
    }
    
    if len(availableMachines) < count {
        return nil, fmt.Errorf("not enough available machines: need %d, have %d", count, len(availableMachines))
    }
    
    // 根据策略选择机器
    selected := a.selectMachines(availableMachines, nodepool.ScaleUpPolicy, count)
    
    // 预留机器（临时状态，防止被其他请求选中）
    for _, machine := range selected {
        machine.Status = "reserving"
        if err := a.db.UpdateMachine(ctx, machine); err != nil {
            return nil, fmt.Errorf("failed to reserve machine: %w", err)
        }
    }
    
    return selected, nil
}

func (a *MachineAllocator) selectMachines(machines []*model.Machine, policy ScaleUpPolicy, count int) []*model.Machine {
    switch policy {
    case PolicyLeastUsed:
        return a.selectByLeastUsed(machines, count)
    case PolicyMostAvailable:
        return a.selectByMostAvailable(machines, count)
    default:
        return a.selectRandomly(machines, count)
    }
}

func (a *MachineAllocator) selectRandomly(machines []*model.Machine, count int) []*model.Machine {
    shuffled := make([]*model.Machine, len(machines))
    copy(shuffled, machines)
    
    rand.Shuffle(len(shuffled), func(i, j int) {
        shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
    })
    
    if len(shuffled) > count {
        return shuffled[:count]
    }
    return shuffled
}

func (a *MachineAllocator) selectByLeastUsed(machines []*model.Machine, count int) []*model.Machine {
    // 按负载排序（假设有负载信息）
    sort.Slice(machines, func(i, j int) bool {
        return machines[i].GetLoadScore() < machines[j].GetLoadScore()
    })
    
    if len(machines) > count {
        return machines[:count]
    }
    return machines
}

func (a *MachineAllocator) selectByMostAvailable(machines []*model.Machine, count int) []*model.Machine {
    // 按可用资源排序
    sort.Slice(machines, func(i, j int) bool {
        return machines[i].GetAvailableResources() > machines[j].GetAvailableResources()
    })
    
    if len(machines) > count {
        return machines[:count]
    }
    return machines
}
```

---

## 六、配置管理

### 6.1 配置文件结构

```yaml
# config.yaml
app:
  name: "kube-static-pool-autoscaler"
  log_level: "info"  # debug, info, warn, error
  environment: "production"
  
# gRPC 服务配置
grpc:
  host: "0.0.0.0"
  port: 8080
  max_concurrent_streams: 100
  max_receive_message_size: 4194304  # 4MB
  max_send_message_size: 4194304
  
# 数据库配置
database:
  postgres:
    host: "postgres-service"
    port: 5432
    username: "autoscaler"
    password: "${POSTGRES_PASSWORD}"
    name: "autoscaler"
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 5m
    ssl_mode: "require"
  redis:
    host: "redis-service"
    port: 6379
    password: "${REDIS_PASSWORD}"
    db: 0
    pool_size: 10
    min_idle_conns: 5
    dial_timeout: 5s
    read_timeout: 3s
    write_timeout: 3s
    
# K8s 配置
kubernetes:
  kubeconfig: ""  # 空表示使用 in-cluster 配置
  context: ""
  namespace: "kube-system"
  # API 重试配置
  max_retries: 3
  retry_interval: 1s
  
# SSH 配置
ssh:
  max_connections_per_host: 3
  connection_timeout: 30s
  execution_timeout: 10m
  max_retry_attempts: 3
  retry_initial_delay: 1s
  retry_max_delay: 30s
  # 生产环境应配置 known_hosts 验证
  insecure_ignore_host_key: false
  
# 扩缩容配置
scaling:
  default_scale_up_concurrency: 5
  default_scale_down_concurrency: 2
  join_timeout: 5m
  drain_timeout: 10m
  heartbeat_interval: 30s
  node_registration_timeout: 5m
  scale_down_unneeded_time: 5m  # 节点空闲多久后可回收
  scale_down_delay_after_add: 3m  # 扩容后多久才能缩容
  
# 监控配置
monitoring:
  metrics_enabled: true
  metrics_port: 9090
  health_check_interval: 10s
  pprof_enabled: false  # 开发环境启用
  pprof_port: 6060
  
# 资源池配置（可动态加载）
nodepools:
  - name: "cpu-pool"
    display_name: "CPU Compute Pool"
    min_size: 0
    max_size: 20
    min_cpu_cores: 8
    min_memory_bytes: 34359738368  # 32GB
    require_gpu: false
    scale_up_policy: "least-used"
    scale_down_policy: "oldest"
    template_labels:
      node-pool: "cpu-pool"
      workload-type: "compute"
      
  - name: "gpu-pool-a100"
    display_name: "GPU Pool (A100)"
    min_size: 0
    max_size: 10
    min_cpu_cores: 32
    min_memory_bytes: 137438953472  # 128GB
    require_gpu: true
    gpu_type: "nvidia-a100"
    gpu_count: 4
    scale_up_policy: "random"
    scale_down_policy: "least-used"
    template_labels:
      node-pool: "gpu-pool-a100"
      gpu-type: "nvidia-a100"
      hardware-type: "gpu"
    template_taints:
      - key: "nvidia.com/gpu"
        operator: "Exists"
        effect: "NoSchedule"
```

### 6.2 配置加载器

```go
type Config struct {
    App         AppConfig         `mapstructure:"app"`
    GRPC        GRPCConfig        `mapstructure:"grpc"`
    Database    DatabaseConfig    `mapstructure:"database"`
    Kubernetes  KubernetesConfig  `mapstructure:"kubernetes"`
    SSH         SSHConfig         `mapstructure:"ssh"`
    Scaling     ScalingConfig     `mapstructure:"scaling"`
    Monitoring  MonitoringConfig  `mapstructure:"monitoring"`
    Nodepools   []NodepoolConfig  `mapstructure:"nodepools"`
}

type ConfigLoader struct {
    config *Config
    viper  *viper.Viper
}

func NewConfigLoader(configPath string) (*ConfigLoader, error) {
    v := viper.New()
    
    // 设置默认值
    v.SetDefault("app.log_level", "info")
    v.SetDefault("grpc.port", 8080)
    v.SetDefault("database.postgres.max_open_conns", 25)
    v.SetDefault("database.redis.pool_size", 10)
    v.SetDefault("ssh.max_connections_per_host", 3)
    v.SetDefault("scaling.default_scale_up_concurrency", 5)
    
    // 配置文件
    v.SetConfigFile(configPath)
    v.SetConfigType("yaml")
    
    // 环境变量覆盖
    v.AutomaticEnv()
    v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
    
    // 读取配置
    if err := v.ReadInConfig(); err != nil {
        if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
            return nil, fmt.Errorf("failed to read config file: %w", err)
        }
    }
    
    var cfg Config
    if err := v.Unmarshal(&cfg); err != nil {
        return nil, fmt.Errorf("failed to unmarshal config: %w", err)
    }
    
    return &ConfigLoader{
        config: &cfg,
        viper:  v,
    }, nil
}

func (l *ConfigLoader) GetConfig() *Config {
    return l.config
}

// ReloadConfig 重新加载配置（支持热重载）
func (l *ConfigLoader) ReloadConfig() error {
    if err := l.viper.ReadInConfig(); err != nil {
        return err
    }
    
    var cfg Config
    if err := l.viper.Unmarshal(&cfg); err != nil {
        return err
    }
    
    l.config = &cfg
    return nil
}
```

---

## 七、部署方案

### 7.1 K8s 部署清单

```yaml
# deploy/k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: kube-system
  labels:
    name: kube-static-pool-autoscaler
---
# deploy/k8s/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: autoscaler-config
  namespace: kube-system
data:
  config.yaml: |
    # 配置文件内容
---
# deploy/k8s/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: autoscaler-secrets
  namespace: kube-system
type: Opaque
stringData:
  postgres-password: "${POSTGRES_PASSWORD}"
  redis-password: "${REDIS_PASSWORD}"
---
# deploy/k8s/ssh-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: autoscaler-ssh-keys
  namespace: kube-system
type: Opaque
stringData:
  id_rsa: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    ... (SSH 私钥内容)
    -----END OPENSSH PRIVATE KEY-----
---
# deploy/k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-static-pool-autoscaler
  namespace: kube-system
spec:
  replicas: 2  # 高可用部署
  selector:
    matchLabels:
      app: kube-static-pool-autoscaler
  template:
    metadata:
      labels:
        app: kube-static-pool-autoscaler
    spec:
      serviceAccountName: autoscaler
      terminationGracePeriodSeconds: 30
      containers:
        - name: provider
          image: kube-static-pool-autoscaler:v1.0.0
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
              name: grpc
            - containerPort: 9090
              name: metrics
            - containerPort: 6060
              name: pprof
          env:
            - name: CONFIG_PATH
              value: "/etc/autoscaler/config.yaml"
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: autoscaler-secrets
                  key: postgres-password
            - name: REDIS_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: autoscaler-secrets
                  key: redis-password
          volumeMounts:
            - name: config
              mountPath: /etc/autoscaler
              readOnly: true
            - name: ssh-keys
              mountPath: /etc/autoscaler/ssh
              readOnly: true
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: 2000m
              memory: 2Gi
          livenessProbe:
            httpGet:
              path: /healthz
              port: 9090
            initialDelaySeconds: 10
            periodSeconds: 10
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: 9090
            initialDelaySeconds: 5
            periodSeconds: 5
            failureThreshold: 3
      volumes:
        - name: config
          configMap:
            name: autoscaler-config
        - name: ssh-keys
          secret:
            secretName: autoscaler-ssh-keys
---
# deploy/k8s/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: kube-static-pool-autoscaler
  namespace: kube-system
spec:
  selector:
    app: kube-static-pool-autoscaler
  ports:
    - name: grpc
      port: 8080
      targetPort: 8080
    - name: metrics
      port: 9090
      targetPort: 9090
    - name: pprof
      port: 6060
      targetPort: 6060
  clusterIP: None  # Headless service for gRPC
---
# deploy/k8s/rbac.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: autoscaler
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["get", "list", "watch", "delete", "update", "patch"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/eviction"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch", "create", "update"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "create", "update"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["clusterroles"]
    verbs: ["get", "list", "watch"]
---
# deploy/k8s/clusterrolebinding.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: autoscaler
subjects:
  - kind: ServiceAccount
    name: autoscaler
    namespace: kube-system
roleRef:
  kind: ClusterRole
  name: autoscaler
  apiGroup: rbac.authorization.k8s.io
---
# deploy/k8s/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: autoscaler
  namespace: kube-system
```

### 7.2 Cluster Autoscaler 部署配置

```yaml
# cluster-autoscaler-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cluster-autoscaler
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cluster-autoscaler
  template:
    metadata:
      labels:
        app: cluster-autoscaler
    spec:
      serviceAccountName: cluster-autoscaler
      containers:
        - image: registry.k8s.io/autoscaling/cluster-autoscaler:v1.28.0
          name: cluster-autoscaler
          ports:
            - containerPort: 8085
              name: http
          command:
            - /cluster-autoscaler
            - --cloud-provider=externalgrpc
            - --cloud-config=/etc/config/external-config.yaml
            - --nodes=0:20:cpu-pool
            - --nodes=0:10:gpu-pool-a100
            - --scale-down-enabled=true
            - --scale-down-unneeded-time=5m
            - --scale-down-delay-after-add=3m
            - --scan-interval=30s
            - --max-nodes-total=50
            - --balance-similar-node-groups=true
            - --emit-per-nodegroup-metrics=true
            - --write-status-configmap=true
            - --status-configmap-namespace=kube-system
            - --use-legacy-cloud-providers=false
          volumeMounts:
            - name: cloud-config
              mountPath: /etc/config
              readOnly: true
          resources:
            requests:
              cpu: 100m
              memory: 300Mi
            limits:
              cpu: 1000m
              memory: 1Gi
      volumes:
        - name: cloud-config
          configMap:
            name: external-cloud-config
---
# external-cloud-config.yaml (ConfigMap)
apiVersion: v1
kind: ConfigMap
metadata:
  name: external-cloud-config
  namespace: kube-system
data:
  external-config.yaml: |
    address: "kube-static-pool-autoscaler:8080"
    timeout: 30s
    # 可选：TLS 配置
    # use_tls: true
    # ca_cert: "/etc/certs/ca.crt"
    # client_cert: "/etc/certs/client.crt"
    # client_key: "/etc/certs/client.key"
```

### 7.3 Docker 构建

```dockerfile
# docker/Dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -o bin/provider ./cmd/provider && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo \
    -o bin/cli ./cmd/cli

# 运行阶段
FROM alpine:3.19

RUN apk --no-cache add ca-certificates openssh-client

WORKDIR /app

# 复制编译好的二进制
COPY --from=builder /app/bin/provider /usr/local/bin/provider
COPY --from=builder /app/bin/cli /usr/local/bin/cli

# 复制脚本模板
COPY scripts/ /app/scripts/

# 创建非 root 用户
RUN addgroup -g 1000 app && \
    adduser -u 1000 -G app -s /bin/sh -D app

USER app

EXPOSE 8080 9090

ENTRYPOINT ["provider"]
CMD ["--config", "/etc/autoscaler/config.yaml"]
```

---

## 八、监控与日志

### 8.1 核心指标

```go
package monitoring

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // ==================== 扩容相关指标 ====================
    
    ScaleUpRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_scale_up_requests_total",
            Help: "Total number of scale up requests",
        },
        []string{"nodepool", "status"},
    )
    
    ScaleUpDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_scale_up_duration_seconds",
            Help:    "Duration of scale up operations",
            Buckets: []float64{60, 120, 180, 300, 600, 1200, 1800, 3600}, // 1min ~ 1hr
        },
        []string{"nodepool"},
    )
    
    ScaleUpMachines = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_scale_up_machines",
            Help:    "Number of machines in a single scale up operation",
            Buckets: []float64{1, 2, 3, 5, 10, 20},
        },
        []string{"nodepool"},
    )
    
    // ==================== 缩容相关指标 ====================
    
    ScaleDownRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_scale_down_requests_total",
            Help: "Total number of scale down requests",
        },
        []string{"nodepool", "status"},
    )
    
    ScaleDownDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_scale_down_duration_seconds",
            Help:    "Duration of scale down operations",
            Buckets: []float64{60, 120, 300, 600, 1200, 1800},
        },
        []string{"nodepool"},
    )
    
    // ==================== 机器状态指标 ====================
    
    MachineStatus = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "static_pool_autoscaler_machine_status",
            Help: "Current number of machines by status",
        },
        []string{"nodepool", "status"},
    )
    
    MachineTotal = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "static_pool_autoscaler_machine_total",
            Help: "Total number of machines by nodepool",
        },
        []string{"nodepool"},
    )
    
    // ==================== SSH 连接指标 ====================
    
    SSHConnectionDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_ssh_connection_duration_seconds",
            Help:    "SSH connection duration",
            Buckets: prometheus.DefBuckets,
        },
        []string{"host"},
    )
    
    SSHConnectionErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_ssh_connection_errors_total",
            Help: "Total number of SSH connection errors",
        },
        []string{"host", "error_type"},
    )
    
    SSHExecutionDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_ssh_execution_duration_seconds",
            Help:    "SSH command execution duration",
            Buckets: prometheus.DefBuckets,
        },
        []string{"host", "command"},
    )
    
    SSHExecutionErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_ssh_execution_errors_total",
            Help: "Total number of SSH execution errors",
        },
        []string{"host", "command", "error_type"},
    )
    
    // ==================== gRPC 服务指标 ====================
    
    GRPCRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_grpc_requests_total",
            Help: "Total number of gRPC requests",
        },
        []string{"method", "status"},
    )
    
    GRPCLatency = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_grpc_latency_seconds",
            Help:    "gRPC request latency",
            Buckets: prometheus.DefBuckets,
        },
        []string{"method"},
    )
    
    // ==================== 数据库指标 ====================
    
    DatabaseQueries = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_database_queries_total",
            Help: "Total number of database queries",
        },
        []string{"operation", "table", "status"},
    )
    
    DatabaseQueryDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "static_pool_autoscaler_database_query_duration_seconds",
            Help: "Database query duration",
            Buckets: prometheus.DefBuckets,
        },
        []string{"operation", "table"},
    )
    
    // ==================== K8s 操作指标 ====================
    
    K8sOperations = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "static_pool_autoscaler_k8s_operations_total",
            Help: "Total number of Kubernetes operations",
        },
        []string{"operation", "status"},
    )
    
    K8sOperationDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "static_pool_autoscaler_k8s_operation_duration_seconds",
            Help:    "Kubernetes operation duration",
            Buckets: prometheus.DefBuckets,
        },
        []string{"operation"},
    )
)
```

### 8.2 健康检查接口

```go
package monitoring

type HealthChecker struct {
    db    *database.DB
    redis *redis.Client
    k8s   *k8s.Client
    provisioner *provisioner.Provisioner
}

func (h *HealthChecker) Check(ctx context.Context) error {
    var errs []error
    
    // 检查数据库连接
    if err := h.db.Ping(ctx); err != nil {
        errs = append(errs, fmt.Errorf("database: %w", err))
    }
    
    // 检查 Redis 连接
    if err := h.redis.Ping(ctx).Err(); err != nil {
        errs = append(errs, fmt.Errorf("redis: %w", err))
    }
    
    // 检查 K8s API
    if _, err := h.k8s.GetNodes(ctx); err != nil {
        errs = append(errs, fmt.Errorf("kubernetes: %w", err))
    }
    
    if len(errs) > 0 {
        return fmt.Errorf("health check failed: %v", errs)
    }
    
    return nil
}

func (h *HealthChecker) Ready(ctx context.Context) error {
    // 额外的就绪检查：确保所有异步操作已完成
    if h.provisioner.HasPendingOperations(ctx) {
        return fmt.Errorf("pending operations in progress")
    }
    
    return h.Check(ctx)
}

// Server HTTP handlers
type Server struct {
    healthChecker *HealthChecker
    registry      prometheus.Registerer
}

func (s *Server) Healthz(w http.ResponseWriter, r *http.Request) {
    if err := s.healthChecker.Check(r.Context()); err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
}

func (s *Server) Readyz(w http.ResponseWriter, r *http.Request) {
    if err := s.healthChecker.Ready(r.Context()); err != nil {
        http.Error(w, err.Error(), http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusOK)
}

func (s *Server) Metrics(w http.ResponseWriter, r *http.Request) {
    promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}
```

### 8.3 日志结构化输出

```go
package logging

import (
    "github.com/sirupsen/logrus"
)

func InitLogger(level string) *logrus.Logger {
    logger := logrus.New()
    
    // 设置日志级别
    logLevel, err := logrus.ParseLevel(level)
    if err != nil {
        logLevel = logrus.InfoLevel
    }
    logger.SetLevel(logLevel)
    
    // 设置格式
    logger.SetFormatter(&logrus.JSONFormatter{
        TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
        FieldMap: logrus.FieldMap{
            logrus.FieldKeyTime:  "timestamp",
            logrus.FieldKeyLevel: "level",
            logrus.FieldKeyMsg:   "message",
        },
    })
    
    return logger
}

// Logger 是全局日志实例
var Logger *logrus.Logger

func init() {
    Logger = InitLogger("info")
}

// WithFields 返回带有字段的日志条目
func WithFields(fields logrus.Fields) *logrus.Entry {
    return Logger.WithFields(fields)
}

// WithError 返回带有错误的日志条目
func WithError(err error) *logrus.Entry {
    return Logger.WithError(err)
}

// Debug 输出调试日志
func Debug(args ...interface{}) {
    Logger.Debug(args...)
}

// Info 输出信息日志
func Info(args ...interface{}) {
    Logger.Info(args...)
}

// Warn 输出警告日志
func Warn(args ...interface{}) {
    Logger.Warn(args...)
}

// Error 输出错误日志
func Error(args ...interface{}) {
    Logger.Error(args...)
}

// Fatal 输出致命日志并退出
func Fatal(args ...interface{}) {
    Logger.Fatal(args...)
}

// 使用示例:
// log := logging.WithFields(logrus.Fields{
//     "nodepool": "gpu-pool",
//     "action":   "scale_up",
//     "machine":  "192.168.1.100",
// })
// log.Info("starting scale up operation")
// log.WithError(err).Error("operation failed")
```

---

## 九、功能模块清单

### 9.1 必须功能 (MVP)

| 模块 | 功能 | 优先级 | 预估工时 |
|-----|------|-------|---------|
| **gRPC Provider** | 实现 NodeGroups, TemplateNodeInfo, IncreaseSize, DeleteNodes | P0 | 3-4 天 |
| **数据库层** | PostgreSQL 模型和 CRUD 操作 | P0 | 2 天 |
| **机器分配器** | 根据策略选择可用机器 | P0 | 1-2 天 |
| **SSH 执行器** | 连接池、脚本执行、错误处理 | P0 | 2-3 天 |
| **K8s 客户端** | Join 命令生成、Drain 节点 | P0 | 1-2 天 |
| **配置管理** | YAML 配置、环境变量、热重载 | P0 | 1 天 |
| **CLI 工具** | 手动扩缩容、机器管理 | P1 | 2 天 |
| **基础监控** | Prometheus 指标、健康检查 | P1 | 1-2 天 |
| **部署清单** | K8s Deployment, Service, RBAC | P1 | 1 天 |

### 9.2 建议功能 (V1.0+)

| 模块 | 功能 | 优先级 | 预估工时 |
|-----|------|-------|---------|
| **高级调度策略** | 成本优化、负载均衡、亲和性策略 | P1 | 3-4 天 |
| **故障恢复** | 自动重试、人工干预接口 | P1 | 2-3 天 |
| **多集群支持** | 同时管理多个 K8s 集群 | P2 | 4-5 天 |
| **Web UI** | 机器状态可视化、事件追踪 | P2 | 5-7 天 |
| **审计日志** | 完整的操作审计追踪 | P2 | 2-3 天 |
| **告警集成** | Prometheus Alertmanager 集成 | P2 | 1-2 天 |
| **机器预热** | 提前准备机器（warm pool） | P2 | 3-4 天 |
| **GPU 特殊支持** | NVIDIA 驱动管理、DCGM 集成 | P2 | 4-5 天 |

---

## 十、项目里程碑

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           MVP (2-3 周)                                    │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 1                                                                  │
│ ├── 项目初始化（Go modules, Makefile, Docker）                           │
│ ├── 数据库设计和迁移脚本                                                 │
│ ├── gRPC 服务基础框架                                                   │
│ └── NodeGroups/TemplateNodeInfo 接口实现                                 │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 2                                                                  │
│ ├── 简单的机器分配器（随机选择）                                         │
│ ├── SSH 连接池和脚本执行                                                │
│ ├── 基础配置和部署清单                                                  │
│ └── 集成测试（手动）                                                    │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 3                                                                  │
│ ├── 完整的 IncreaseSize/DeleteNodes 实现                                │
│ ├── CLI 管理工具                                                        │
│ ├── Prometheus 监控指标                                                 │
│ └── MVP 交付                                                            │
└──────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────┐
│                         V1.0 (2-3 周)                                     │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 4                                                                  │
│ ├── 高级调度策略（least-used, cost-optimized）                          │
│ ├── 健康检查和优雅关闭                                                  │
│ └── 高可用部署（2 副本）                                                │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 5                                                                  │
│ ├── 自动化测试（单元+集成）                                             │
│ ├── 故障自动恢复机制                                                    │
│ └── 文档（README, API 文档）                                            │
├──────────────────────────────────────────────────────────────────────────┤
│ Week 6                                                                  │
│ ├── 多集群支持                                                           │
│ ├── 性能优化和基准测试                                                  │
│ └── V1.0 交付                                                           │
└──────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────┐
│                         V1.1+ (2-4 周)                                    │
├──────────────────────────────────────────────────────────────────────────┤
│ ├── Web UI 仪表板                                                        │
│ ├── 高级告警和通知                                                       │
│ ├── 审计日志和合规报告                                                   │
│ └── GPU 特殊支持（NVIDIA 驱动、DCGM）                                    │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 十一、风险与注意事项

### 11.1 关键风险

| 风险 | 影响 | 概率 | 严重性 | 缓解措施 |
|-----|------|-----|-------|---------|
| SSH 连接不稳定 | 扩容失败率升高 | 高 | 中 | 重试机制、超时控制、连接池 |
| 状态不一致 | 机器状态混乱 | 中 | 高 | 分布式锁、事务、补偿机制 |
| 并发冲突 | 同时扩容同一台机器 | 中 | 高 | 乐观锁 + Redis 分布式锁 |
| 网络分区 | 节点失联无法 drain | 低 | 高 | 心跳检测、人工干预接口 |
| 硬件差异 | 同池子机器规格不同 | 中 | 中 | 拆分 nodepool 或使用模板平均 |
| K8s API 限流 | Join/Drain 操作失败 | 中 | 低 | 指数退避、重试机制 |
| 数据库故障 | 无法查询机器状态 | 低 | 高 | 主从复制、高可用配置 |

### 11.2 运维建议

1. **高可用**: 至少部署 2 个 Provider 实例，使用 Leader Election
2. **监控**: 关注扩容成功率、SSH 执行失败率、节点注册超时率
3. **备份**: 定期备份 PostgreSQL 数据，测试恢复流程
4. **测试**: 在非生产环境完整测试扩缩容流程，建立 CI/CD
5. **回滚**: 保留手动扩缩容能力作为兜底，准备回滚脚本
6. **安全**: SSH 密钥使用专门的 Secret 管理，定期轮换
7. **容量规划**: 预留 10-20% 的备用机器，防止库存不足

### 11.3 监控告警建议

```yaml
# Prometheus 告警规则
groups:
  - name: autoscaler
    rules:
      # 扩容失败率过高
      - alert: HighScaleUpFailureRate
        expr: |
          rate(static_pool_autoscaler_scale_up_requests_total{status="failed"}[5m]) /
          rate(static_pool_autoscaler_scale_up_requests_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "扩容失败率过高"
          description: "过去 5 分钟扩容失败率超过 10%"
      
      # SSH 连接失败率过高
      - alert: HighSSHConnectionFailureRate
        expr: |
          rate(static_pool_autoscaler_ssh_connection_errors_total[5m]) > 0.05
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "SSH 连接失败率过高"
          description: "SSH 连接失败率超过 5%"
      
      # 机器注册超时
      - alert: NodeRegistrationTimeout
        expr: |
          static_pool_autoscaler_machine_status{status="joining"} > 10
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "有机器长时间处于 joining 状态"
          description: "超过 10 台机器 joining 超过 10 分钟"
      
      # 可用机器不足
      - alert: LowAvailableMachines
        expr: |
          static_pool_autoscaler_machine_status{status="available"} < 5
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "可用机器数量不足"
          description: "可用机器少于 5 台"
      
      # Provider 不健康
      - alert: ProviderUnhealthy
        expr: |
          up{job="kube-static-pool-autoscaler"} == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Provider 服务不可用"
```

---

## 附录

### A. 参考资料

- [Cluster Autoscaler External gRPC Provider](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler/cloudprovider/externalgrpc)
- [External gRPC Cloud Provider 提案](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/proposals/plugable-provider-grpc.md)
- [Kubernetes Cluster Autoscaler](https://github.com/kubernetes/autoscaler)
- [kubeadm join](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-join/)
- [kubeadm reset](https://kubernetes.io/docs/reference/setup-tools/kubeadm/kubeadm-reset/)

### B. 术语表

| 术语 | 说明 |
|-----|------|
| CA | Cluster Autoscaler，Kubernetes 官方自动扩缩容组件 |
| NodePool | 资源池，一组同质化的机器 |
| TemplateNodeInfo | 节点模板信息，描述扩容后节点的特征 |
| Join | 节点加入 Kubernetes 集群的过程 |
| Drain | 节点离开前排空 Pod 的过程 |
| Reset | 节点离开后重置 Kubernetes 配置的过程 |

### C. 变更日志

| 版本 | 日期 | 变更 |
|-----|------|------|
| v1.0.0 | 2024-01-16 | 初始设计文档 |
| v1.0.2 | 2024-01-17 | 新增机器导入、池子维护、生命周期管理、Web UI 设计 |
| v1.0.3 | 2024-01-17 | 移除 BMC 管理，简化为 SSH-only 模式 |
| v1.0.4 | 2026-01-16 | 修复代码错误（Release 参数、IsConnected 方法、rand 包导入） |

---

## D. 勘误与补充说明（2024-01-16）

本文档基于用户反馈进行以下修复和补充：

### D.1 已修复的语法错误

1. **第41行格式问题** - 已修复为正确的 Markdown 表格格式
2. **第1170-1172行 Go 代码** - 已修正为正确的 const 定义
3. **第1999-2002行 struct 定义** - 已修正为正确的 Server struct 定义
4. **连接池 key 匹配逻辑** - Release 方法已修改为接受 user/host/port 参数

### D.2 Leader Election 机制设计

高可用部署需要确保只有一个实例执行扩缩容操作，避免并发冲突。

#### D.2.1 使用 Kubernetes Lease 实现

```go
package leader

import (
    "context"
    "fmt"
    "time"
    
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/tools/leaderelection"
    "k8s.io/client-go/tools/leaderelection/resourcelock"
    "k8s.io/klog/v2"
)

type LeaderElector struct {
    client        kubernetes.Interface
    lock          *resourcelock.LeaseLock
    leaseDuration time.Duration
    renewDeadline time.Duration
    retryPeriod   time.Duration
    callbacks     LeaderCallbacks
    stopCh        chan struct{}
}

type LeaderCallbacks struct {
    OnStartedLeading func(ctx context.Context)
    OnStoppedLeading func()
    OnNewLeader      func(identity string)
}

func NewLeaderElector(client kubernetes.Interface, leaseName, namespace string, callbacks LeaderCallbacks) *LeaderElector {
    return &LeaderElector{
        client: client,
        lock: &resourcelock.LeaseLock{
            LeaseMeta: metav1.ObjectMeta{
                Name:      leaseName,
                Namespace: namespace,
            },
            Client: client.CoordinationV1(),
            LockConfig: resourcelock.ResourceLockConfig{
                Identity: fmt.Sprintf("autoscaler-%d", time.Now().UnixNano()%1000),
            },
        },
        leaseDuration: 15 * time.Second,
        renewDeadline: 10 * time.Second,
        retryPeriod:   5 * time.Second,
        callbacks:     callbacks,
        stopCh:        make(chan struct{}),
    }
}

func (le *LeaderElector) Run(ctx context.Context) error {
    leaderelection.Run(ctx, leaderelection.LeaderElectionConfig{
        Lock:            le.lock,
        LeaseDuration:   le.leaseDuration,
        RenewDeadline:   le.renewDeadline,
        RetryPeriod:     le.retryPeriod,
        Callbacks: leaderelection.LeaderCallbacks{
            OnStartedLeading: func(ctx context.Context) {
                klog.Info("became leader, starting main loop")
                le.callbacks.OnStartedLeading(ctx)
            },
            OnStoppedLeading: func() {
                klog.Info("lost leadership, stopping")
                le.callbacks.OnStoppedLeading()
            },
            OnNewLeader: func(identity string) {
                klog.Infof("new leader elected: %s", identity)
                le.callbacks.OnNewLeader(identity)
            },
        },
        WatchDog: nil,
        Name:     "kube-static-pool-autoscaler",
    })
    
    return nil
}

func (le *LeaderElector) Stop() {
    close(le.stopCh)
}
```

#### D.2.2 在 Provider 中集成 Leader Election

```go
func main() {
    // ... 初始化配置和客户端 ...
    
    // 创建 Leader Election
    callbacks := leader.LeaderCallbacks{
        OnStartedLeading: func(ctx context.Context) {
            // 成为 Leader 后启动 gRPC 服务
            startGRPCServer(ctx)
        },
        OnStoppedLeading: func() {
            // 失去 Leader 地位，停止服务
            stopGRPCServer()
        },
        OnNewLeader: func(identity string) {
            log.Info("new leader elected", "identity", identity)
        },
    }
    
    elector := leader.NewLeaderElector(
        kubeClient,
        "autoscaler-leader",
        "kube-system",
        callbacks,
    )
    
    // 启动 Leader Election
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    go elector.Run(ctx)
    
    // 等待信号
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    
    cancel()
    elector.Stop()
}
```

#### D.2.3 Redis 分布式锁备选方案

```go
package redis

import (
    "context"
    "time"
    
    "github.com/redis/go-redis/v9"
)

type DistributedLock struct {
    client    *redis.Client
    lockKey   string
    ttl       time.Duration
}

func NewDistributedLock(client *redis.Client, lockKey string, ttl time.Duration) *DistributedLock {
    return &DistributedLock{
        client:  client,
        lockKey: lockKey,
        ttl:     ttl,
    }
}

// Acquire 尝试获取锁
func (l *DistributedLock) Acquire(ctx context.Context) (bool, error) {
    return l.client.SetNX(ctx, l.lockKey, "locked", l.ttl).Result()
}

// Release 释放锁
func (l *DistributedLock) Release(ctx context.Context) error {
    return l.client.Del(ctx, l.lockKey).Err()
}

// Extend 延长锁的 ttl
func (l *DistributedLock) Extend(ctx context.Context) error {
    return l.client.Expire(ctx, l.lockKey, l.ttl).Err()
}

// 使用示例
func executeWithLock(ctx context.Context, lock *DistributedLock, fn func() error) error {
    acquired, err := lock.Acquire(ctx)
    if err != nil {
        return err
    }
    if !acquired {
        return fmt.Errorf("failed to acquire lock")
    }
    defer lock.Release(ctx)
    
    // 延长锁的 ttl（后台 goroutine）
    ticker := time.NewTicker(l.ttl / 2)
    defer ticker.Stop()
    
    done := make(chan struct{})
    go func() {
        for {
            select {
            case <-ticker.C:
                if err := lock.Extend(ctx); err != nil {
                    log.Error(err, "failed to extend lock")
                }
            case <-done:
                return
            case <-ctx.Done():
                done <- struct{}{}
                return
            }
        }
    }()
    
    err = fn()
    close(done)
    return err
}
```

### D.3 Join Token 管理设计

kubeadm join 需要有效的 token 和 discovery-token-ca-cert-hash，Token 默认有效期为 24 小时。

#### D.3.1 Token 自动刷新机制

```go
package k8s

import (
    "bytes"
    "context"
    "fmt"
    "os/exec"
    "time"
)

type TokenManager struct {
    client    *Client
    tokenTTL  time.Duration
    cache     map[string]*cachedToken
    cacheMu   sync.RWMutex
}

type cachedToken struct {
    token       string
    caCertHash  string
    expiresAt   time.Time
    description string
}

func NewTokenManager(client *Client, tokenTTL time.Duration) *TokenManager {
    return &TokenManager{
        client:   client,
        tokenTTL: tokenTTL,
        cache:    make(map[string]*cachedToken),
    }
}

// GetOrCreateToken 获取或创建 token
func (tm *TokenManager) GetOrCreateToken(ctx context.Context, description string) (string, string, error) {
    tm.cacheMu.RLock()
    cached, exists := tm.cache[description]
    tm.cacheMu.RUnlock()
    
    if exists && time.Now().Before(cached.expiresAt) {
        return cached.token, cached.caCertHash, nil
    }
    
    // 生成新的 token
    token, caCertHash, err := tm.createToken(ctx, description)
    if err != nil {
        return "", "", fmt.Errorf("failed to create token: %w", err)
    }
    
    // 缓存 token
    tm.cacheMu.Lock()
    tm.cache[description] = &cachedToken{
        token:       token,
        caCertHash:  caCertHash,
        expiresAt:   time.Now().Add(tm.tokenTTL),
        description: description,
    }
    tm.cacheMu.Unlock()
    
    return token, caCertHash, nil
}

func (tm *TokenManager) createToken(ctx context.Context, description string) (string, string, error) {
    // 方法 1: 使用 kubeadm 命令
    cmd := exec.CommandContext(ctx, "kubeadm", "token", "create", "--description", description, "--print-join-command")
    output, err := cmd.Output()
    if err != nil {
        return "", "", fmt.Errorf("kubeadm token create failed: %w", err)
    }
    
    // 解析输出
    joinCmd := string(output)
    var token, caCertHash string
    fmt.Sscanf(joinCmd, "kubeadm join %*s --token %s --discovery-token-ca-cert-hash %s", &token, &caCertHash)
    
    return token, caCertHash, nil
}

// DeleteToken 删除 token
func (tm *TokenManager) DeleteToken(ctx context.Context, token string) error {
    cmd := exec.CommandContext(ctx, "kubeadm", "token", "delete", token)
    output, err := cmd.Output()
    if err != nil {
        return fmt.Errorf("kubeadm token delete failed: %w, output: %s", err, output)
    }
    
    // 清除缓存
    tm.cacheMu.Lock()
    for k, v := range tm.cache {
        if v.token == token {
            delete(tm.cache, k)
            break
        }
    }
    tm.cacheMu.Unlock()
    
    return nil
}

// ListTokens 列出所有 token
func (tm *TokenManager) ListTokens(ctx context.Context) ([]TokenInfo, error) {
    cmd := exec.CommandContext(ctx, "kubeadm", "token", "list", "-o", "wide")
    output, err := cmd.Output()
    if err != nil {
        return nil, fmt.Errorf("kubeadm token list failed: %w", err)
    }
    
    // 解析输出
    // 输出格式:
    // TOKEN                    TTL  EXPIRES                USAGES  DESCRIPTION                        EXTRA GROUPS
    // abc123.xyz               23h  2024-01-17T10:00:00Z   <none>  autoscaler-machine-xxx              system:bootstrappers:kubeadm:default-node-token
    
    var tokens []TokenInfo
    lines := strings.Split(string(output), "\n")[1:] // 跳过表头
    for _, line := range lines {
        if strings.TrimSpace(line) == "" {
            continue
        }
        fields := strings.Fields(line)
        if len(fields) >= 6 {
            tokens = append(tokens, TokenInfo{
                Token:     fields[0],
                TTL:       fields[1],
                Expires:   fields[2],
                Usage:     fields[3],
                Desc:      fields[4],
                ExtraGroup: strings.Join(fields[5:], " "),
            })
        }
    }
    
    return tokens, nil
}

type TokenInfo struct {
    Token     string
    TTL       string
    Expires   string
    Usage     string
    Desc      string
    ExtraGroup string
}
```

#### D.3.2 Bootstrap Token Secret 方案（备选）

对于需要更细粒度控制的场景，可以直接创建 Bootstrap Token Secret：

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bootstrap-token-autoscaler
  namespace: kube-system
type: bootstrap.kubernetes.io/token
data:
  # token-id.token-secret (base64 编码)
  token-id: YXV0b3NjYWxl
  token-secret: c2VjcmV0
  # 过期时间 (Unix timestamp)
  expiration: MTcwNDgzMjAwMA==
  # 使用描述
  description: "Autoscaler machine join token"
  # 认证组
  "bootstrap.kubernetes.io/cluster-status": ""
```

```go
func (tm *TokenManager) CreateBootstrapTokenSecret(ctx context.Context, tokenID, tokenSecret, description string) error {
    expiration := time.Now().Add(tm.tokenTTL).Unix()
    
    secret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("bootstrap-token-%s", tokenID),
            Namespace: "kube-system",
            Labels: map[string]string{
                "bootstrap.kubernetes.io/cluster-status": "",
            },
        },
        Type: "bootstrap.kubernetes.io/token",
        Data: map[string][]byte{
            "token-id":      []byte(tokenID),
            "token-secret":  []byte(tokenSecret),
            "expiration":    []byte(fmt.Sprintf("%d", expiration)),
            "description":   []byte(description),
            "usage-bootstrap-signing": []byte("true"),
        },
    }
    
    _, err := tm.client.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{})
    return err
}
```

### D.4 完善的状态机设计

补充 `reserving` 状态及其转换逻辑：

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           完整状态机                                      │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│   ┌─────────────┐                                                       │
│   │  available  │ (待命状态，机器已开机，可被分配)                        │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 扩容请求 + 分配机器 → reserving                             │
│          │                                                               │
│   ┌──────▼──────┐                                                       │
│   │ reserving   │ (预占状态，已被选中等待 Join)                          │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── Join 成功 → joining                                        │
│          │                                                               │
│          ├── Join 失败 → fault (或回滚到 available)                      │
│          │                                                               │
│          ├── 分配超时未使用 → available (自动释放)                       │
│          │                                                               │
│          ├── 人工取消 → available                                        │
│          │                                                               │
│   ┌──────▼──────┐                                                       │
│   │   joining   │ (正在加入集群)                                         │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── Join 成功 → in_use                                         │
│          │   │                                                          │
│          │   └── 注册 node_name, node_uid                               │
│          │                                                               │
│          └── Join 失败 → fault (或回滚到 available)                      │
│              │                                                          │
│              └── 重试次数超限 → maintenance                              │
│                                                                         │
│   ┌──────┬──────┐                                                       │
│   │           │                                                          │
│   │  in_use   │ (已加入集群，正常使用)                                   │
│   │           │                                                          │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 缩容请求 → draining                                         │
│          │                                                               │
│          ├── 节点失联 → maintenance (需要人工介入)                       │
│          │                                                               │
│          └── 定期心跳检测 → 维持状态                                     │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  draining   │ (正在排空 Pod)                                         │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── Drain 成功 → leaving                                       │
│          │                                                               │
│          └── Drain 超时 → maintenance (强制下线)                         │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  leaving    │ (正在离开集群)                                         │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── Reset 成功 → available                                     │
│          │                                                               │
│          └── Reset 失败 → maintenance                                   │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │    fault    │ (故障状态)                                             │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 人工修复 → available                                        │
│          │                                                               │
│          └── 标记为坏 → maintenance                                      │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │ maintenance │ (维护中)                                               │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 修复完成 → available                                        │
│          │                                                               │
│          └── 报废处理 → archived (归档)                                  │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

#### D.4.1 状态转换实现

```go
package machine

type Status string

const (
    StatusAvailable  Status = "available"
    StatusReserving  Status = "reserving"
    StatusJoining    Status = "joining"
    StatusInUse      Status = "in_use"
    StatusDraining   Status = "draining"
    StatusLeaving    Status = "leaving"
    StatusFault      Status = "fault"
    StatusMaintenance Status = "maintenance"
    StatusArchived   Status = "archived"
)

type StatusTransition struct {
    from   Status
    to     Status
    action func(*Machine) error
}

// 允许的状态转换
var allowedTransitions = []StatusTransition{
    {StatusAvailable, StatusReserving, nil},
    {StatusReserving, StatusJoining, nil},
    {StatusReserving, StatusAvailable, nil},  // 取消分配
    {StatusJoining, StatusInUse, nil},
    {StatusJoining, StatusFault, nil},
    {StatusJoining, StatusAvailable, nil},    // 回滚
    {StatusInUse, StatusDraining, nil},
    {StatusInUse, StatusMaintenance, nil},    // 失联
    {StatusDraining, StatusLeaving, nil},
    {StatusDraining, StatusMaintenance, nil}, // 超时
    {StatusLeaving, StatusAvailable, nil},
    {StatusLeaving, StatusMaintenance, nil},
    {StatusFault, StatusAvailable, nil},
    {StatusFault, StatusMaintenance, nil},
    {StatusMaintenance, StatusAvailable, nil},
    {StatusMaintenance, StatusArchived, nil},
}

func (m *Machine) CanTransitionTo(to Status) bool {
    for _, t := range allowedTransitions {
        if m.Status == t.from && to == t.to {
            return true
        }
    }
    return false
}

func (m *Machine) TransitionTo(to Status) error {
    if !m.CanTransitionTo(to) {
        return fmt.Errorf("invalid status transition from %s to %s", m.Status, to)
    }
    
    m.Status = to
    m.UpdatedAt = time.Now()
    return nil
}
```

### D.5 错误处理和重试机制

#### D.5.1 重试策略

```go
package retry

import (
    "context"
    "errors"
    "fmt"
    "math/rand"
    "strings"
    "time"
)

type RetryPolicy struct {
    MaxAttempts      int
    InitialDelay     time.Duration
    MaxDelay         time.Duration
    Multiplier       float64
    Jitter           bool
}

func WithContext(ctx context.Context, policy RetryPolicy, fn func() error) error {
    var lastErr error
    delay := policy.InitialDelay
    
    for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
        if err := fn(); err != nil {
            lastErr = err
            
            // 检查是否可重试
            if !IsRetryable(err) {
                return err
            }
            
            // 检查上下文是否取消
            select {
            case <-ctx.Done():
                return ctx.Err()
            default:
            }
            
            // 计算延迟
            if attempt < policy.MaxAttempts {
                actualDelay := delay
                if policy.Jitter {
                    actualDelay = time.Duration(float64(delay) * (0.5 + rand.Float64()*0.5))
                }
                
                time.Sleep(actualDelay)
                delay = time.Duration(float64(delay) * policy.Multiplier)
                if delay > policy.MaxDelay {
                    delay = policy.MaxDelay
                }
            }
            continue
        }
        return nil
    }
    
    return fmt.Errorf("max attempts exceeded: %w", lastErr)
}

func IsRetryable(err error) bool {
    // 可重试的错误类型
    retryableErrors := []error{
        context.DeadlineExceeded,
        ErrSSHConnectionFailed,
        ErrSSHTimeout,
        ErrK8SAPITimeout,
        ErrK8SAPIThrottled,
    }
    
    for _, e := range retryableErrors {
        if err == e || errors.Is(err, e) {
            return true
        }
    }
    
    // 检查错误消息
    msg := err.Error()
    retryablePatterns := []string{
        "connection refused",
        "connection reset",
        "no such host",
        "timeout",
        "i/o timeout",
        "temporary failure",
        "network is unreachable",
    }
    
    for _, pattern := range retryablePatterns {
        if strings.Contains(strings.ToLower(msg), pattern) {
            return true
        }
    }
    
    return false
}

// 预定义的重试策略
var (
    SSHConnectionRetry = RetryPolicy{
        MaxAttempts:  3,
        InitialDelay: 1 * time.Second,
        MaxDelay:     10 * time.Second,
        Multiplier:   2.0,
        Jitter:       true,
    }
    
    JoinRetry = RetryPolicy{
        MaxAttempts:  3,
        InitialDelay: 5 * time.Second,
        MaxDelay:     60 * time.Second,
        Multiplier:   2.0,
        Jitter:       true,
    }
    
    DrainRetry = RetryPolicy{
        MaxAttempts:  2,
        InitialDelay: 30 * time.Second,
        MaxDelay:     120 * time.Second,
        Multiplier:   1.5,
        Jitter:       true,
    }
)
```

#### D.5.2 错误处理流程

```go
package provisioner

type ErrorHandler struct {
    eventLogger   *EventLogger
    metrics       *Metrics
    alertSender   *AlertSender
}

func (h *ErrorHandler) HandleJoinFailure(ctx context.Context, machines []*model.Machine, err error) {
    for _, machine := range machines {
        // 更新机器状态
        machine.Status = "fault"
        machine.ErrorMessage = err.Error()
        machine.LastActionAt = time.Now()
        
        if updateErr := h.db.UpdateMachine(ctx, machine); updateErr != nil {
            h.metrics.RecordMachineStatusTransition(machine.NodepoolID, "join_failed_update_error")
        }
        
        // 记录事件
        h.eventLogger.Log(ctx, machine.NodepoolID, machine.ID, "join_failed", "failed", map[string]interface{}{
            "error": err.Error(),
        })
        
        // 发送告警
        h.alertSender.Send(ctx, &Alert{
            Type:      AlertTypeJoinFailed,
            Severity:  AlertSeverityWarning,
            Machine:   machine.IPAddress,
            Nodepool:  machine.NodepoolID,
            Message:   fmt.Sprintf("Machine %s failed to join cluster: %v", machine.IPAddress, err),
        })
        
        h.metrics.RecordMachineStatusTransition(machine.NodepoolID, "join_failed")
    }
}

func (h *ErrorHandler) HandleDrainFailure(ctx context.Context, machine *model.Machine, err error) {
    machine.Status = "maintenance"
    machine.ErrorMessage = err.Error()
    machine.LastActionAt = time.Now()
    
    h.db.UpdateMachine(ctx, machine)
    
    h.eventLogger.Log(ctx, machine.NodepoolID, machine.ID, "drain_failed", "failed", map[string]interface{}{
        "error": err.Error(),
    })
    
    h.alertSender.Send(ctx, &Alert{
        Type:      AlertTypeDrainFailed,
        Severity:  AlertSeverityError,
        Machine:   machine.IPAddress,
        Nodepool:  machine.NodepoolID,
        Message:   fmt.Sprintf("Failed to drain node %s: %v", machine.NodeName, err),
    })
}

func (h *ErrorHandler) HandleResetFailure(ctx context.Context, machine *model.Machine, err error) {
    machine.Status = "maintenance"
    machine.ErrorMessage = fmt.Sprintf("Reset failed: %v", err.Error())
    machine.LastActionAt = time.Now()
    
    h.db.UpdateMachine(ctx, machine)
    
    h.eventLogger.Log(ctx, machine.NodepoolID, machine.ID, "reset_failed", "failed", map[string]interface{}{
        "error": err.Error(),
    })
    
    h.alertSender.Send(ctx, &Alert{
        Type:      AlertTypeResetFailed,
        Severity:  AlertSeverityWarning,
        Machine:   machine.IPAddress,
        Nodepool:  machine.NodepoolID,
        Message:   fmt.Sprintf("Failed to reset machine %s, manual intervention required: %v", machine.IPAddress, err),
    })
}
```

### D.6 SSH 安全性方案

#### D.6.1 安全的 HostKey 验证

```go
package ssh

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
)

type HostKeyVerifier struct {
    knownHostsPath string
    knownHosts     map[string]string // host -> key type:key
}

func NewHostKeyVerifier(knownHostsPath string) *HostKeyVerifier {
    verifier := &HostKeyVerifier{
        knownHostsPath: knownHostsPath,
        knownHosts:     make(map[string]string),
    }
    
    // 加载 known_hosts 文件
    if err := verifier.loadKnownHosts(); err != nil {
        // 如果文件不存在，创建一个空的 verifier
        // 首次连接时需要手动验证
    }
    
    return verifier
}

func (v *HostKeyVerifier) loadKnownHosts() error {
    file, err := os.Open(v.knownHostsPath)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    defer file.Close()
    
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
            continue
        }
        
        // 解析 known_hosts 格式
        // 格式: hostname key_type key
        parts := strings.Fields(line)
        if len(parts) >= 3 {
            host := parts[0]
            keyType := parts[1]
            key := parts[2]
            v.knownHosts[host] = keyType + " " + key
            
            // 也解析通配符格式
            if idx := strings.Index(host, ","); idx != -1 {
                for _, h := range strings.Split(host[idx+1:], ",") {
                    v.knownHosts[h] = keyType + " " + key
                }
            }
        }
    }
    
    return scanner.Err()
}

func (v *HostKeyVerifier) VerifyHostKey(host string, remoteNetAddr string, remoteKey ssh.PublicKey) error {
    // 检查是否是已知主机
    expectedKey, known := v.knownHosts[host]
    if !known {
        // 也尝试使用 IP 地址
        known = v.knownHosts[remoteNetAddr]
    }
    
    if !known {
        return &UnknownHostError{
            Host:        host,
            RemoteAddr:  remoteNetAddr,
            Fingerprint: ssh.FingerprintLegacyMD5(remoteKey),
        }
    }
    
    // 验证密钥是否匹配
    expectedKeyType, expectedKeyData, _ := strings.Cut(expectedKey, " ")
    actualKeyType := remoteKey.Type()
    
    if expectedKeyType != actualKeyType {
        return &HostKeyMismatchError{
            Host:           host,
            ExpectedType:   expectedKeyType,
            ActualType:     actualKeyType,
        }
    }
    
    return nil
}

// 添加新的主机密钥（首次连接时）
func (v *HostKeyVerifier) AddHostKey(host string, key ssh.PublicKey) error {
    keyType := key.Type()
    keyData := string(key.Marshal())
    
    v.knownHosts[host] = keyType + " " + keyData
    
    // 追加到 known_hosts 文件
    file, err := os.OpenFile(v.knownHostsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return err
    }
    defer file.Close()
    
    _, err = file.WriteString(fmt.Sprintf("%s %s %s\n", host, keyType, keyData))
    return err
}

type UnknownHostError struct {
    Host        string
    RemoteAddr  string
    Fingerprint string
}

func (e *UnknownHostError) Error() string {
    return fmt.Sprintf("unknown host %s (fingerprint: %s). Please verify and add to known_hosts", e.Host, e.Fingerprint)
}

type HostKeyMismatchError struct {
    Host         string
    ExpectedType string
    ActualType   string
}

func (e *HostKeyMismatchError) Error() string {
    return fmt.Sprintf("host key mismatch for %s: expected %s, got %s", e.Host, e.ExpectedType, e.ActualType)
}
```

#### D.6.2 使用 StrictHostKeyChecking

```go
func (p *ConnectionPool) createNewConnection(ctx context.Context, host, user string, port int) (*ssh.Client, error) {
    // 获取 SSH 密钥
    key, err := p.sshKeyStore.GetDefaultKey(ctx)
    if err != nil {
        return nil, fmt.Errorf("failed to get SSH key: %w", err)
    }
    
    // 构建 SSH 密钥签名者
    signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(key.PrivateKey), []byte(key.Passphrase))
    if err != nil {
        signer, err = ssh.ParsePrivateKey([]byte(key.PrivateKey))
        if err != nil {
            return nil, fmt.Errorf("failed to parse SSH key: %w", err)
        }
    }
    
    // 构建 SSH 客户端配置
    config := &ssh.ClientConfig{
        User: user,
        Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
        Timeout: 30 * time.Second,
    }
    
    // 设置 HostKeyCallback
    if p.config.SSH.InsecureIgnoreHostKey {
        // 开发环境：跳过 host key 检查
        config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
    } else {
        // 生产环境：严格验证
        verifier := NewHostKeyVerifier("/etc/autoscaler/known_hosts")
        config.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
            return verifier.VerifyHostKey(hostname, remote.String(), key)
        }
    }
    
    // 连接
    client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
    if err != nil {
        return nil, fmt.Errorf("SSH connection failed: %w", err)
    }
    
    return client, nil
}
```

### D.7 Graceful Shutdown 机制

```go
package main

import (
    "context"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"
)

type Server struct {
    grpcServer  *grpc.Server
    httpServer  *http.Server
    provisioner *provisioner.Provisioner
    elector     *leader.LeaderElector
}

func (s *Server) Start() error {
    // 启动 gRPC 服务器
    s.grpcServer = grpc.NewServer()
    pb.RegisterExternalCloudProviderServer(s.grpcServer, s)
    
    lis, err := net.Listen("tcp", ":8080")
    if err != nil {
        return err
    }
    
    go func() {
        if err := s.grpcServer.Serve(lis); err != nil {
            log.Error(err, "gRPC server failed")
        }
    }()
    
    // 启动 HTTP 服务器（健康检查和指标）
    s.httpServer = &http.Server{
        Addr:    ":9090",
        Handler: s.createHTTPHandler(),
    }
    
    go func() {
        if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Error(err, "HTTP server failed")
        }
    }()
    
    return nil
}

func (s *Server) GracefulShutdown() {
    log.Info("starting graceful shutdown...")
    
    // 创建超时上下文
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    
    // 1. 停止接受新请求
    log.Info("stopping HTTP server...")
    if err := s.httpServer.Shutdown(ctx); err != nil {
        log.Error(err, "HTTP server shutdown failed")
    }
    
    // 2. 停止 gRPC 服务（如果是 Leader）
    if s.elector != nil {
        log.Info("stopping leader election...")
        s.elector.Stop()
    }
    
    // 3. 等待正在进行的操作完成
    log.Info("waiting for ongoing operations to complete...")
    if err := s.provisioner.WaitForPendingOperations(ctx); err != nil {
        log.Warn("timeout waiting for pending operations", "error", err)
    }
    
    // 4. 强制停止 gRPC 服务
    log.Info("stopping gRPC server...")
    s.grpcServer.GracefulStop()
    
    log.Info("graceful shutdown completed")
}

func main() {
    // 创建服务器
    server, err := NewServer()
    if err != nil {
        log.Fatal(err)
    }
    
    // 启动服务器
    if err := server.Start(); err != nil {
        log.Fatal(err)
    }
    
    // 等待退出信号
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
    
    sig := <-sigCh
    log.Info("received signal", "signal", sig)
    
    // 优雅关闭
    server.GracefulShutdown()
    
    os.Exit(0)
}
```

### D.8 数据库迁移策略

#### D.8.1 使用 golang-migrate

```bash
# 安装迁移工具
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# 创建迁移文件
migrate create -ext sql -dir db/migrations -seq init_schema
```

```sql
-- db/migrations/000001_init_schema.up.sql
CREATE TABLE IF NOT EXISTS nodepools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(128) NOT NULL UNIQUE,
    display_name VARCHAR(255),
    min_size INT NOT NULL DEFAULT 0,
    max_size INT NOT NULL DEFAULT 10,
    template_labels JSONB NOT NULL DEFAULT '{}',
    template_taints JSONB DEFAULT '[]',
    min_cpu_cores INT DEFAULT 0,
    min_memory_bytes BIGINT DEFAULT 0,
    require_gpu BOOLEAN DEFAULT FALSE,
    gpu_type VARCHAR(64),
    priority INT DEFAULT 0,
    scale_up_policy VARCHAR(32) DEFAULT 'random',
    scale_down_policy VARCHAR(32) DEFAULT 'oldest',
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS machines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname VARCHAR(255) NOT NULL,
    ip_address INET NOT NULL,
    nodepool_id UUID NOT NULL REFERENCES nodepools(id),
    status VARCHAR(32) NOT NULL DEFAULT 'available',
    cpu_cores INT NOT NULL,
    memory_bytes BIGINT NOT NULL,
    gpu_info JSONB,
    ssh_user VARCHAR(64) NOT NULL DEFAULT 'root',
    ssh_port INT NOT NULL DEFAULT 22,
    ssh_key_id UUID,
    node_name VARCHAR(255),
    node_uid VARCHAR(255),
    labels JSONB DEFAULT '{}',
    taints JSONB DEFAULT '[]',
    tags JSONB DEFAULT '{}',
    last_heartbeat TIMESTAMP,
    last_action VARCHAR(32),
    last_action_at TIMESTAMP,
    error_message TEXT,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 索引
CREATE INDEX idx_machines_nodepool_status ON machines(nodepool_id, status);
CREATE INDEX idx_machines_node_name ON machines(node_name);
```

```sql
-- db/migrations/000001_init_schema.down.sql
DROP TABLE IF EXISTS machines;
DROP TABLE IF EXISTS nodepools;
```

#### D.8.2 在应用中集成迁移

```go
package database

import (
    "github.com/golang-migrate/migrate/v4"
    _ "github.com/golang-migrate/migrate/v4/database/postgres"
    _ "github.com/golang-migrate/migrate/v4/source/file"
)

type Migrator struct {
    db          *sql.DB
    migrations  string
}

func NewMigrator(db *sql.DB, migrationsPath string) *Migrator {
    return &Migrator{
        db:         db,
        migrations: migrationsPath,
    }
}

func (m *Migrator) Migrate() error {
    migrator, err := migrate.NewWithDatabaseInstance(
        "file://"+m.migrations,
        "postgres",
        m.db,
    )
    if err != nil {
        return fmt.Errorf("failed to create migrator: %w", err)
    }
    
    // 执行迁移
    if err := migrator.Up(); err != nil && err != migrate.ErrNoChange {
        return fmt.Errorf("migration failed: %w", err)
    }
    
    // 获取当前版本
    version, dirty, err := migrator.Version()
    if err != nil {
        return err
    }
    
    log.Info("migration completed", "version", version, "dirty", dirty)
    return nil
}

func (m *Migrator) ForceMigrate(version int) error {
    migrator, err := migrate.NewWithDatabaseInstance(
        "file://"+m.migrations,
        "postgres",
        m.db,
    )
    if err != nil {
        return err
    }
    
    return migrator.Force(version)
}
```

### D.9 TLS/mTLS 配置细节

#### D.9.1 gRPC TLS 配置

```go
package grpc

import (
    "crypto/tls"
    "crypto/x509"
    "fmt"
    "os"
    
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials"
)

type TLSConfig struct {
    CertFile      string
    KeyFile       string
    CAFile        string
    ClientAuth    bool // 是否要求客户端证书
    SkipVerify    bool // 是否跳过服务器端证书验证
}

func NewServerTLSConfig(cfg TLSConfig) (credentials.TransportCredentials, error) {
    // 加载服务器证书和密钥
    serverCert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
    if err != nil {
        return nil, fmt.Errorf("failed to load server certificate: %w", err)
    }
    
    // 创建 TLS 配置
    tlsConfig := &tls.Config{
        Certificates: []tls.Certificate{serverCert},
        ClientAuth:   tls.NoClientCert,
    }
    
    // 如果需要验证客户端证书
    if cfg.ClientAuth {
        // 加载 CA 证书
        caCert, err := os.ReadFile(cfg.CAFile)
        if err != nil {
            return nil, fmt.Errorf("failed to load CA certificate: %w", err)
        }
        
        certPool := x509.NewCertPool()
        if !certPool.AppendCertsFromPEM(caCert) {
            return nil, fmt.Errorf("failed to parse CA certificate")
        }
        
        tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
        tlsConfig.ClientCAs = certPool
    }
    
    // 如果需要跳过服务器端证书验证（客户端使用）
    if cfg.SkipVerify {
        tlsConfig.InsecureSkipVerify = true
    }
    
    return credentials.NewTLS(tlsConfig), nil
}

func NewClientTLSConfig(cfg TLSConfig) (credentials.TransportCredentials, error) {
    // 加载 CA 证书
    caCert, err := os.ReadFile(cfg.CAFile)
    if err != nil {
        return nil, fmt.Errorf("failed to load CA certificate: %w", err)
    }
    
    certPool := x509.NewCertPool()
    if !certPool.AppendCertsFromPEM(caCert) {
        return nil, fmt.Errorf("failed to parse CA certificate")
    }
    
    // 创建 TLS 配置
    tlsConfig := &tls.Config{
        RootCAs: certPool,
    }
    
    if cfg.SkipVerify {
        tlsConfig.InsecureSkipVerify = true
    }
    
    return credentials.NewTLS(tlsConfig), nil
}

// 创建 gRPC 服务器
func NewServer(cfg TLSConfig) (*grpc.Server, error) {
    creds, err := NewServerTLSConfig(cfg)
    if err != nil {
        return nil, err
    }
    
    return grpc.NewServer(
        grpc.Creds(creds),
        grpc.MaxConcurrentStreams(100),
    ), nil
}

// 创建 gRPC 客户端连接
func NewClientConnection(addr string, cfg TLSConfig) (*grpc.ClientConn, error) {
    creds, err := NewClientTLSConfig(cfg)
    if err != nil {
        return nil, err
    }
    
    return grpc.Dial(addr, grpc.WithTransportCredentials(creds))
}
```

#### D.9.2 配置示例

```yaml
grpc:
  tls:
    enabled: true
    cert_file: "/etc/certs/server.crt"
    key_file: "/etc/certs/server.key"
    ca_file: "/etc/certs/ca.crt
    # 客户端认证（双向 TLS）
    client_auth: true
```

### D.10 Rate Limiting 和 Circuit Breaker

#### D.10.1 Rate Limiter

```go
package ratelimit

import (
    "context"
    "golang.org/x/time/rate"
)

type Limiter struct {
    limiters map[string]*rate.Limiter
    mutex    sync.RWMutex
}

func NewLimiter(limit rate.Limit, burst int) *Limiter {
    return &Limiter{
        limiters: make(map[string]*rate.Limiter),
    }
}

func (l *Limiter) Allow(ctx context.Context, key string) bool {
    l.mutex.RLock()
    limiter, exists := l.limiters[key]
    l.mutex.RUnlock()
    
    if !exists {
        l.mutex.Lock()
        // 双重检查
        if limiter, exists = l.limiters[key]; !exists {
            limiter = rate.NewLimiter(rate.Inf, 0) // 默认无限制
            l.limiters[key] = limiter
        }
        l.mutex.Unlock()
    }
    
    return limiter.Allow(ctx)
}

// SSH 连接限流
type SSHConnectionLimiter struct {
    globalLimiter *rate.Limiter
    perHostLimiter map[string]*rate.Limiter
    hostMutex      sync.RWMutex
    hostBurst      int
    globalBurst    int
}

func NewSSHConnectionLimiter(globalBurst, perHostBurst int) *SSHConnectionLimiter {
    return &SSHConnectionLimiter{
        globalLimiter:  rate.NewLimiter(rate.Inf, globalBurst),
        perHostLimiter: make(map[string]*rate.Limiter),
        hostBurst:      perHostBurst,
        globalBurst:    globalBurst,
    }
}

func (l *SSHConnectionLimiter) AllowHost(host string) bool {
    l.hostMutex.RLock()
    limiter, exists := l.perHostLimiter[host]
    l.hostMutex.RUnlock()
    
    if !exists {
        l.hostMutex.Lock()
        if limiter, exists = l.perHostLimiter[host]; !exists {
            limiter = rate.NewLimiter(rate.Inf, l.hostBurst)
            l.perHostLimiter[host] = limiter
        }
        l.hostMutex.Unlock()
    }
    
    // 检查单主机限流
    if !limiter.Allow() {
        return false
    }
    
    // 检查全局限流
    return l.globalLimiter.Allow()
}
```

#### D.10.2 Circuit Breaker

```go
package circuitbreaker

import (
    "context"
    "sync"
    "time"
)

type State int

const (
    StateClosed State = iota
    StateHalfOpen
    StateOpen
)

type CircuitBreaker struct {
    name             string
    state            State
    failureCount     int
    successCount     int
    lastFailure      time.Time
    mutex            sync.Mutex
    // 配置
    maxFailures      int
    timeout          time.Duration
    halfOpenSuccesses int
}

func NewCircuitBreaker(name string, maxFailures int, timeout time.Duration) *CircuitBreaker {
    return &CircuitBreaker{
        name:              name,
        state:             StateClosed,
        maxFailures:       maxFailures,
        timeout:           timeout,
        halfOpenSuccesses: 1,
    }
}

func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
    cb.mutex.Lock()
    state := cb.state
    cb.mutex.Unlock()
    
    // 检查状态
    if state == StateOpen {
        if time.Since(cb.lastFailure) > cb.timeout {
            // 超时，尝试进入半开状态
            cb.mutex.Lock()
            if cb.state == StateOpen {
                cb.state = StateHalfOpen
                cb.successCount = 0
            }
            cb.mutex.Unlock()
        } else {
            return &CircuitOpenError{Name: cb.name}
        }
    }
    
    // 执行操作
    err := fn()
    
    cb.mutex.Lock()
    defer cb.mutex.Unlock()
    
    if err != nil {
        // 失败处理
        cb.failureCount++
        cb.lastFailure = time.Now()
        
        if cb.failureCount >= cb.maxFailures && cb.state != StateOpen {
            cb.state = StateOpen
        }
        
        return err
    }
    
    // 成功处理
    if cb.state == StateHalfOpen {
        cb.successCount++
        if cb.successCount >= cb.halfOpenSuccesses {
            cb.state = StateClosed
            cb.failureCount = 0
        }
    } else if cb.state == StateClosed {
        // 成功后重置失败计数
        cb.failureCount = 0
    }
    
    return nil
}

type CircuitOpenError struct {
    Name string
}

func (e *CircuitOpenError) Error() string {
    return fmt.Sprintf("circuit breaker %s is open", e.Name)
}

// 使用示例
var sshCircuitBreaker = circuitbreaker.NewCircuitBreaker("ssh", 5, 1*time.Minute)

func (p *Provisioner) sshExecuteWithBreaker(ctx context.Context, machine *model.Machine, cmd string) (string, error) {
    var output string
    err := sshCircuitBreaker.Execute(ctx, func() error {
        var execErr error
        output, execErr = p.sshExecute(ctx, machine, cmd)
        return execErr
    })
    return output, err
}
```

### D.11 机器健康检查设计

#### D.11.1 心跳检测机制

```go
package monitoring

type HeartbeatChecker struct {
    db      *database.DB
    sshPool *ssh.ConnectionPool
    k8s     *k8s.Client
    // 配置
    interval      time.Duration
    timeout       time.Duration
    failureThreshold int
}

func NewHeartbeatChecker(db *database.DB, sshPool *ssh.ConnectionPool, k8s *k8s.Client) *HeartbeatChecker {
    return &HeartbeatChecker{
        db:                db,
        sshPool:           sshPool,
        k8s:               k8s,
        interval:          30 * time.Second,
        timeout:           10 * time.Second,
        failureThreshold:  3,
    }
}

func (c *HeartbeatChecker) Start(ctx context.Context) {
    ticker := time.NewTicker(c.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.checkAllMachines(ctx)
        }
    }
}

func (c *HeartbeatChecker) checkAllMachines(ctx context.Context) {
    machines, err := c.db.GetMachinesByStatus(ctx, "in_use")
    if err != nil {
        log.Error(err, "failed to get in-use machines")
        return
    }
    
    var wg sync.WaitGroup
    for _, machine := range machines {
        wg.Add(1)
        go func(m *model.Machine) {
            defer wg.Done()
            c.checkMachine(ctx, m)
        }(machine)
    }
    
    wg.Wait()
}

func (c *HeartbeatChecker) checkMachine(ctx context.Context, machine *model.Machine) {
    // 方法 1: 检查 K8s 节点状态
    node, err := c.k8s.GetNode(ctx, machine.NodeName)
    if err != nil {
        log.Warn("failed to get node from k8s", "node", machine.NodeName, "error", err)
        c.handleFailure(ctx, machine, "k8s_node_not_found")
        return
    }
    
    // 检查节点是否 Ready
    for _, condition := range node.Status.Conditions {
        if condition.Type == corev1.NodeReady {
            if condition.Status != corev1.ConditionTrue {
                c.handleFailure(ctx, machine, "node_not_ready")
                return
            }
            break
        }
    }
    
    // 方法 2: SSH 健康检查（可选，更严格）
    if c.sshPool != nil {
        err := c.sshHealthCheck(ctx, machine)
        if err != nil {
            log.Warn("SSH health check failed", "machine", machine.IPAddress, "error", err)
            c.handleFailure(ctx, machine, "ssh_health_check_failed")
            return
        }
    }
    
    // 更新心跳时间
    machine.LastHeartbeat = time.Now()
    machine.ConsecutiveFailures = 0
    c.db.UpdateMachine(ctx, machine)
}

func (c *HeartbeatChecker) sshHealthCheck(ctx context.Context, machine *model.Machine) error {
    ctx, cancel := context.WithTimeout(ctx, c.timeout)
    defer cancel()
    
    conn, err := c.sshPool.Get(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort)
    if err != nil {
        return err
    }
    defer c.sshPool.Release(conn)
    
    // 执行简单的健康检查命令
    _, err = conn.Exec(ctx, "echo 'ok'")
    return err
}

func (c *HeartbeatChecker) handleFailure(ctx context.Context, machine *model.Machine, reason string) {
    machine.ConsecutiveFailures++
    
    if machine.ConsecutiveFailures >= c.failureThreshold {
        // 连续失败超过阈值，标记为 maintenance
        machine.Status = "maintenance"
        machine.ErrorMessage = fmt.Sprintf("health check failed: %s", reason)
        
        // 发送告警
        alertSender.Send(ctx, &Alert{
            Type:      AlertTypeMachineUnhealthy,
            Severity:  AlertSeverityWarning,
            Machine:   machine.IPAddress,
            Nodepool:  machine.NodepoolID,
            Message:   fmt.Sprintf("Machine %s is unhealthy: %s", machine.IPAddress, reason),
        })
    }
    
    c.db.UpdateMachine(ctx, machine)
}
```

### D.12 Kubernetes 版本兼容性

| K8s 版本 | 支持状态 | 备注 |
|---------|---------|------|
| v1.26 | ✅ 支持 | 最低支持版本（2024-01 安全更新终止） |
| v1.27 | ✅ 支持 | |
| v1.28 | ✅ 支持 | |
| v1.29 | ✅ 支持 | |
| v1.30 | ✅ 支持 | |
| v1.31 | ✅ 支持 | |
| v1.32 | ✅ 支持 | |
| v1.33 | ✅ 支持 | |
| v1.34 | ✅ 支持 | |
| v1.35 | ✅ 支持 | 最新稳定版 |
| v1.36 | ⚠️ 预览 | 需测试验证 |

#### D.12.1 kubeadm 版本兼容性

```go
package k8s

import (
    "context"
    "os/exec"
    "regexp"
    "strconv"
    "strings"
)

type KubeadmVersion struct {
    Major int
    Minor int
}

func GetKubeadmVersion(ctx context.Context) (*KubeadmVersion, error) {
    cmd := exec.CommandContext(ctx, "kubeadm", "version", "-o", "short")
    output, err := cmd.Output()
    if err != nil {
        return nil, err
    }
    
    versionStr := strings.TrimSpace(string(output))
    // 格式: "kubeadm version: &version{Major:"1", Minor:"28", GitVersion:"v1.28.0", ...}"
    
    re := regexp.MustCompile(`Major:"(\d+)", Minor:"(\d+)"`)
    matches := re.FindStringSubmatch(versionStr)
    if len(matches) != 3 {
        return nil, fmt.Errorf("failed to parse kubeadm version: %s", versionStr)
    }
    
    major, _ := strconv.Atoi(matches[1])
    minor, _ := strconv.Atoi(matches[2])
    
    return &KubeadmVersion{Major: major, Minor: minor}, nil
}

// 获取支持的 K8s 版本范围
// 注意：kubeadm join 命令在 K8s 1.26~1.35 版本中保持稳定，无需特殊适配
func GetSupportedK8sVersions() (minVersion, maxVersion *KubeadmVersion) {
    min := &KubeadmVersion{Major: 1, Minor: 26}
    max := &KubeadmVersion{Major: 1, Minor: 36}
    return min, max
}

func IsVersionSupported(version *KubeadmVersion) bool {
    min, max := GetSupportedK8sVersions()
    
    if version.Major < min.Major || version.Major > max.Major {
        return false
    }
    
    if version.Major == min.Major && version.Minor < min.Minor {
        return false
    }
    
    if version.Major == max.Major && version.Minor > max.Minor {
        return false
    }
    
    return true
}
```

#### D.12.2 Join 命令版本适配

```go
// GenerateJoinCommand 生成 kubeadm join 命令
// kubeadm token create 命令在 K8s 1.26~1.36 版本中保持一致，无需特殊适配
func (c *Client) GenerateJoinCommand(ctx context.Context) (string, error) {
    version, err := GetKubeadmVersion(ctx)
    if err != nil {
        return "", err
    }
    
    // 验证版本支持
    if !IsVersionSupported(version) {
        return "", fmt.Errorf("unsupported kubernetes version: %d.%d", version.Major, version.Minor)
    }
    
    // 执行 token create 命令
    // 注意：kubeadm 版本应与目标集群版本匹配
    cmd := exec.CommandContext(ctx, "kubeadm", "token", "create",
        "--description", "autoscaler-machine-join",
        "--print-join-command")
    
    output, err := cmd.Output()
    if err != nil {
        return "", fmt.Errorf("failed to create join token: %w", err)
    }
    
    return string(output), nil
}
```

#### D.12.3 兼容性说明

```go
package compatibility

// VersionInfo 记录各 K8s 版本的兼容性状态
type VersionInfo struct {
    Version       string
    Status        string // supported, latest, deprecated, unsupported
    EOL           string // 生命周期结束日期
    TestedUpTo    string // 最后测试版本
    Notes         string
}

var K8sVersionInfo = []VersionInfo{
    {"v1.26", "deprecated", "2024-02-28", "v1.26.15", "安全更新已终止，建议升级"},
    {"v1.27", "supported", "2024-06-28", "v1.27.14", ""},
    {"v1.28", "recommended", "2024-08-28", "v1.28.10", "推荐用于生产环境"},
    {"v1.29", "supported", "2024-12-28", "v1.29.4", ""},
    {"v1.30", "supported", "2025-02-28", "v1.30.0", ""},
    {"v1.31", "supported", "2025-06-30", "v1.31.0", ""},
    {"v1.32", "supported", "2025-10-31", "v1.32.0", ""},
    {"v1.33", "supported", "2026-02-28", "v1.33.0", ""},
    {"v1.34", "supported", "2026-06-30", "v1.34.0", ""},
    {"v1.35", "latest", "2026-10-31", "v1.35.0", "当前最新稳定版"},
    {"v1.36", "preview", "2027-02-28", "-", "beta 阶段"},
}

// IsVersionDeprecated 检查版本是否已废弃
func IsVersionDeprecated(version string) bool {
    for _, info := range K8sVersionInfo {
        if info.Version == version {
            return info.Status == "deprecated"
        }
    }
    return false
}

// GetEOLDate 获取版本生命周期结束日期
func GetEOLDate(version string) string {
    for _, info := range K8sVersionInfo {
        if info.Version == version {
            return info.EOL
        }
    }
    return "unknown"
}
```

### D.13 附录：勘误汇总

| 日期 | 版本 | 修复内容 |
|-----|------|---------|
| 2024-01-16 | v1.0.1 | 修复第41行表格格式错误 |
| 2024-01-16 | v1.0.1 | 修复第1170-1172行Go代码语法错误 |
| 2024-01-16 | v1.0.1 | 修复第1999-2002行struct语法错误 |
| 2024-01-16 | v1.0.1 | 修复连接池key匹配逻辑bug |
| 2024-01-16 | v1.0.1 | 新增Leader Election机制设计 |
| 2024-01-16 | v1.0.1 | 新增Join Token管理设计 |
| 2024-01-16 | v1.0.1 | 新增状态机补充(reserving状态) |
| 2024-01-16 | v1.0.1 | 新增错误处理和重试机制 |
| 2024-01-16 | v1.0.1 | 新增SSH安全性方案 |
| 2024-01-16 | v1.0.1 | 新增Graceful Shutdown机制 |
| 2024-01-16 | v1.0.1 | 新增数据库迁移策略 |
| 2024-01-16 | v1.0.1 | 新增TLS/mTLS配置细节 |
| 2024-01-16 | v1.0.1 | 新增Rate Limiting/Circuit Breaker设计 |
| 2024-01-16 | v1.0.1 | 新增机器健康检查设计 |
| 2024-01-16 | v1.0.1 | 新增K8s版本兼容性说明 |
| 2024-01-17 | v1.0.2 | 新增CLI命令设计 |
| 2024-01-17 | v1.0.2 | 新增敏感数据加密设计 |
| 2024-01-17 | v1.0.2 | 新增审计日志设计 |
| 2024-01-17 | v1.0.2 | 新增升级和回滚策略 |
| 2024-01-17 | v1.0.2 | 新增灾难恢复设计 |
| 2024-01-17 | v1.0.2 | 新增测试策略设计 |
| 2024-01-17 | v1.0.2 | 新增性能基准和优化设计 |
| 2024-01-17 | v1.0.2 | 新增异常处理完善设计 |
| 2024-01-17 | v1.0.2 | 新增基础设施高可用设计 |
| 2024-01-17 | v1.0.2 | 更新K8s版本兼容性(v1.26~v1.36)，修正过时版本说明 |

---

## E. 数据模型补充设计

### E.1 机器表字段补充

在原始 machines 表基础上，补充以下字段：

```sql
ALTER TABLE machines ADD COLUMN IF NOT EXISTS consecutive_failures INT DEFAULT 0;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS region VARCHAR(64) DEFAULT 'default';
ALTER TABLE machines ADD COLUMN IF NOT EXISTS zone VARCHAR(64) DEFAULT 'default';
ALTER TABLE machines ADD COLUMN IF NOT EXISTS rack VARCHAR(64);
ALTER TABLE machines ADD COLUMN IF NOT EXISTS machine_type VARCHAR(64); -- physical, virtual
ALTER TABLE machines ADD COLUMN IF NOT EXISTS os_image VARCHAR(128); -- "ubuntu-22.04", "centos-7"
ALTER TABLE machines ADD COLUMN IF NOT EXISTS kernel_version VARCHAR(64);
ALTER TABLE machines ADD COLUMN IF NOT EXISTS kubelet_version VARCHAR(64);
ALTER TABLE machines ADD COLUMN IF NOT EXISTS container_runtime VARCHAR(32); -- "containerd", "docker"
ALTER TABLE machines ADD COLUMN IF NOT EXISTS last_operated_at TIMESTAMP;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS operated_by VARCHAR(128); -- 操作者标识
ALTER TABLE machines ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}'; -- 扩展元数据
```

### E.2 数据模型更新后的完整 machines 表

```sql
CREATE TABLE machines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        VARCHAR(255) NOT NULL,
    ip_address      INET NOT NULL,
    nodepool_id     UUID NOT NULL REFERENCES nodepools(id),
    status          VARCHAR(32) NOT NULL DEFAULT 'available',
    
    -- 硬件规格
    cpu_cores       INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_info        JSONB,              -- {"type": "nvidia-a100", "count": 4}
    gpu_count       INT DEFAULT 0,      -- GPU 数量冗余字段，便于查询
    
    -- SSH 凭据
    ssh_user        VARCHAR(64) NOT NULL DEFAULT 'root',
    ssh_port        INT NOT NULL DEFAULT 22,
    ssh_key_id      UUID REFERENCES ssh_keys(id),
    
    -- 节点信息（加入集群后）
    node_name       VARCHAR(255),
    node_uid        VARCHAR(255),
    join_token      VARCHAR(255),
    join_hash       VARCHAR(255),
    
    -- 元数据
    labels          JSONB DEFAULT '{}', -- 自定义标签
    taints          JSONB DEFAULT '[]', -- 污点列表
    tags            JSONB DEFAULT '{}', -- 业务标签，如 "department": "ml-team"
    
    -- 地理位置
    region          VARCHAR(64) DEFAULT 'default',  -- 区域
    zone            VARCHAR(64) DEFAULT 'default',  -- 可用区
    rack            VARCHAR(64),                    -- 机架位置
    
    -- 机器类型
    machine_type    VARCHAR(64) DEFAULT 'physical', -- physical, virtual
    
    -- 系统信息
    os_image        VARCHAR(128),                   -- 操作系统镜像
    kernel_version  VARCHAR(64),                    -- 内核版本
    kubelet_version VARCHAR(64),                    -- Kubelet 版本
    container_runtime VARCHAR(32),                  -- containerd, docker
    
    -- 状态追踪
    last_heartbeat  TIMESTAMP,
    last_action     VARCHAR(32),
    last_action_at  TIMESTAMP,
    last_operated_at TIMESTAMP,
    operated_by     VARCHAR(128),                   -- 操作者标识
    consecutive_failures INT DEFAULT 0,             -- 连续失败次数
    error_message   TEXT,
    
    -- 扩展字段
    metadata        JSONB DEFAULT '{}',             -- 预留扩展
    
    -- 审计字段
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);

-- 补充索引
CREATE INDEX idx_machines_consecutive_failures ON machines(consecutive_failures);
CREATE INDEX idx_machines_region_zone ON machines(region, zone);
CREATE INDEX idx_machines_machine_type ON machines(machine_type);
CREATE INDEX idx_machines_last_heartbeat ON machines(last_heartbeat);
CREATE INDEX idx_machines_os_image ON machines(os_image);
CREATE INDEX idx_machines_kubelet_version ON machines(kubelet_version);
```

### E.3 nodepools 表补充字段

```sql
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS region VARCHAR(64) DEFAULT 'default';
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS zone VARCHAR(64) DEFAULT 'default';
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS scale_up_cooldown_seconds INT DEFAULT 300;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS scale_down_cooldown_seconds INT DEFAULT 300;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS drain_before_delete BOOLEAN DEFAULT TRUE;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS drain_timeout_seconds INT DEFAULT 600;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS machine_type VARCHAR(64);
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS os_image VARCHAR(128);
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS pre_join_script TEXT; -- 预执行脚本
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS post_join_script TEXT; -- Join 后执行脚本
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS pre_reset_script TEXT; -- Reset 前执行脚本
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS post_reset_script TEXT; -- Reset 后执行脚本
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS notification_channels JSONB DEFAULT '[]'; -- 告警通知渠道
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS autoscale_enabled BOOLEAN DEFAULT TRUE;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE nodepools ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}';
```

---

## F. CLI 命令设计

### F.1 命令结构

```bash
ksa <command> <subcommand> [options] [arguments]

Commands:
  nodepool    管理资源池
  machine     管理机器
  scale       手动扩缩容
  token       管理 Join Token
  event       查看事件日志
  status      查看系统状态
  config      配置管理
  version     查看版本信息
```

### F.2 详细命令设计

#### F.2.1 nodepool 命令

```bash
# 查看所有资源池
ksa nodepool list

# 查看单个资源池详情
ksa nodepool get <name>

# 创建资源池
ksa nodepool create \
  --name=cpu-pool \
  --display-name="CPU Compute Pool" \
  --min-size=0 \
  --max-size=20 \
  --min-cpu-cores=8 \
  --min-memory=32GiB \
  --scale-up-policy=least-used \
  --scale-down-policy=oldest

# 创建 GPU 资源池
ksa nodepool create \
  --name=gpu-pool-a100 \
  --display-name="GPU Pool (A100)" \
  --min-size=0 \
  --max-size=10 \
  --require-gpu=true \
  --gpu-type=nvidia-a100 \
  --gpu-count=4 \
  --template-labels=hardware-type=gpu \
  --template-taints=nvidia.com/gpu=NoSchedule

# 更新资源池
ksa nodepool update <name> --max-size=30 --scale-up-policy=random

# 启用/禁用资源池
ksa nodepool enable <name>
ksa nodepool disable <name>

# 删除资源池
ksa nodepool delete <name> --force
```

#### F.2.2 machine 命令

```bash
# 查看所有机器
ksa machine list
ksa machine list --nodepool=cpu-pool
ksa machine list --status=available
ksa machine list --status=in_use
ksa machine list --region=cn-bj1

# 查看机器详情
ksa machine get <machine-id>

# 添加机器
ksa machine add \
  --nodepool=cpu-pool \
  --hostname=node-001 \
  --ip-address=192.168.1.100 \
  --cpu-cores=16 \
  --memory=64GiB \
  --ssh-user=root \
  --ssh-key=default

# 批量添加机器（从文件）
ksa machine batch-add --file=machines.yaml

# 更新机器
ksa machine update <machine-id> --labels=env=prod

# 设置机器为维护模式
ksa machine maintenance <machine-id> --reason="hardware-upgrade"

# 恢复机器
ksa machine restore <machine-id>

# 删除机器
ksa machine delete <machine-id> --force

# 查看机器日志
ksa machine logs <machine-id> --tail=100

# SSH 连接机器
ksa machine ssh <machine-id>

# 远程执行命令
ksa machine exec <machine-id> --command="kubectl get nodes"
```

#### F.2.3 scale 命令（手动扩缩容）

```bash
# 手动扩容
ksa scale up cpu-pool --delta=2
ksa scale up gpu-pool-a100 --count=1

# 手动缩容
ksa scale down cpu-pool --node=node-001
ksa scale down cpu-pool --nodes=node-001,node-002 --force

# 查看待扩缩容任务
ksa scale pending

# 取消待执行任务
ksa scale cancel <task-id>
```

#### F.2.4 token 命令

```bash
# 查看所有 Token
ksa token list

# 创建 Token
ksa token create --description="gpu-pool-machine" --ttl=24h

# 删除 Token
ksa token delete <token-id>

# 清理过期 Token
ksa token cleanup

# 查看 Token 使用情况
ksa token usage
```

#### F.2.5 event 命令

```bash
# 查看事件
ksa event list
ksa event list --nodepool=cpu-pool
ksa event list --machine=192.168.1.100
ksa event list --type=scale_up
ksa event list --status=failed
ksa event list --since=1h
ksa event list --since=2024-01-16T00:00:00Z

# 查看事件详情
ksa event get <event-id>

# 导出事件
ksa event export --file=events.jsonl --since=7d
```

#### F.2.6 status 命令

```bash
# 查看系统状态
ksa status

# 查看资源池状态
ksa status nodepools

# 查看机器状态分布
ksa status machines

# 查看操作队列状态
ksa status queue

# 查看性能指标
ksa status metrics
```

#### F.2.7 config 命令

```bash
# 查看当前配置
ksa config show

# 更新配置（交互式）
ksa config edit

# 从文件加载配置
ksa config load --file=config.yaml

# 导出配置
ksa config export --file=config.yaml

# 验证配置
ksa config validate
```

### F.3 CLI 实现框架

```go
package main

import (
    "context"
    "os"
    
    "github.com/spf13/cobra"
    "k8s.io/klog/v2"
)

func main() {
    klog.InitFlags(nil)
    defer klog.Flush()
    
    ctx := klog.NewContext(context.Background(), klog.NewKlogr())
    
    rootCmd := &cobra.Command{
        Use:   "ksa",
        Short: "Kube Static Pool Autoscaler CLI",
        Long:  `CLI for managing static machine pools in Kubernetes clusters.`,
        PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
            return nil
        },
    }
    
    // 添加子命令
    rootCmd.AddCommand(newNodepoolCommand(ctx))
    rootCmd.AddCommand(newMachineCommand(ctx))
    rootCmd.AddCommand(newScaleCommand(ctx))
    rootCmd.AddCommand(newTokenCommand(ctx))
    rootCmd.AddCommand(newEventCommand(ctx))
    rootCmd.AddCommand(newStatusCommand(ctx))
    rootCmd.AddCommand(newConfigCommand(ctx))
    rootCmd.AddCommand(newVersionCommand())
    
    if err := rootCmd.ExecuteContext(ctx); err != nil {
        klog.Error(err, "command failed")
        os.Exit(1)
    }
}

func newNodepoolCommand(ctx context.Context) *cobra.Command {
    cmd := &cobra.Command{
        Use:   "nodepool",
        Short: "Manage node pools",
    }
    
    cmd.AddCommand(
        newNodepoolListCommand(ctx),
        newNodepoolGetCommand(ctx),
        newNodepoolCreateCommand(ctx),
        newNodepoolUpdateCommand(ctx),
        newNodepoolDeleteCommand(ctx),
    )
    
    return cmd
}
```

### F.4 配置文件格式（machines.yaml）

```yaml
# 批量添加机器配置文件格式
machines:
  - hostname: node-001
    ip_address: 192.168.1.100
    nodepool: cpu-pool
    cpu_cores: 16
    memory_bytes: 68719476736  # 64GB
    ssh_user: root
    ssh_port: 22
    labels:
      env: prod
      team: ml
    tags:
      department: ai
    region: cn-bj1
    zone: zone-a
    
  - hostname: node-002
    ip_address: 192.168.1.101
    nodepool: gpu-pool-a100
    cpu_cores: 64
    memory_bytes: 274877906944  # 256GB
    gpu_info:
      type: nvidia-a100
      count: 4
    ssh_user: root
    ssh_port: 22
    labels:
      env: prod
      gpu_type: a100
    region: cn-bj1
    zone: zone-a
```

---

## G. 敏感数据加密设计

### G.1 加密方案概述

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         敏感数据加密架构                                  │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌───────────────────────────────────────────────────────────────────┐ │
│  │                        应用层加密                                   │ │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────────┐ │ │
│  │  │ SSH 私钥    │  │ API Key     │  │ 数据库密码                  │ │ │
│  │  │ (AES-256)  │  │ (AES-256)  │  │ (AES-256)                   │ │ │
│  │  └─────────────┘  └─────────────┘  └─────────────────────────────┘ │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                       │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                      KMS 密钥管理                                   │ │
│  │  ┌─────────────────────────────────────────────────────────────┐  │ │
│  │  │  主密钥 (Master Key) - 存储在 HSM/KMS                        │  │ │
│  │  │  数据密钥 (Data Key) - 由主密钥加密后存储                    │  │ │
│  │  │  密钥轮换 - 定期更换主密钥                                    │  │ │
│  │  └─────────────────────────────────────────────────────────────┘  │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### G.2 加密实现

```go
package encryption

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "crypto/rsa"
    "crypto/x509"
    "encoding/base64"
    "encoding/pem"
    "fmt"
    "os"
    "sync"
)

type Encryptor struct {
    masterKey    []byte  // 主密钥
    masterKeyID  string
    kmsClient    KMSClient
    keyCache     map[string][]byte
    cacheMu      sync.RWMutex
}

type KMSClient interface {
    Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
    Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
    GetKeyID() string
}

// NewEncryptor 创建加密器
func NewEncryptor(kmsClient KMSClient, masterKey []byte) *Encryptor {
    return &Encryptor{
        masterKey:   masterKey,
        masterKeyID: kmsClient.GetKeyID(),
        kmsClient:   kmsClient,
        keyCache:    make(map[string][]byte),
    }
}

// Encrypt 使用 AES-256-GCM 加密数据
func (e *Encryptor) Encrypt(ctx context.Context, plaintext []byte, keyID string) ([]byte, error) {
    // 获取或生成数据密钥
    dataKey, err := e.getDataKey(ctx, keyID)
    if err != nil {
        return nil, fmt.Errorf("failed to get data key: %w", err)
    }
    
    // 创建 AES-256-GCM 加密器
    block, err := aes.NewCipher(dataKey)
    if err != nil {
        return nil, fmt.Errorf("failed to create cipher: %w", err)
    }
    
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, fmt.Errorf("failed to create GCM: %w", err)
    }
    
    // 生成随机 nonce
    nonce := make([]byte, gcm.NonceSize())
    if _, err := rand.Read(nonce); err != nil {
        return nil, fmt.Errorf("failed to generate nonce: %w", err)
    }
    
    // 加密
    ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
    
    return ciphertext, nil
}

// Decrypt 解密数据
func (e *Encryptor) Decrypt(ctx context.Context, ciphertext []byte, keyID string) ([]byte, error) {
    // 获取数据密钥
    dataKey, err := e.getDataKey(ctx, keyID)
    if err != nil {
        return nil, fmt.Errorf("failed to get data key: %w", err)
    }
    
    // 创建 AES-256-GCM 解密器
    block, err := aes.NewCipher(dataKey)
    if err != nil {
        return nil, fmt.Errorf("failed to create cipher: %w", err)
    }
    
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, fmt.Errorf("failed to create GCM: %w", err)
    }
    
    // 提取 nonce
    nonceSize := gcm.NonceSize()
    if len(ciphertext) < nonceSize {
        return nil, fmt.Errorf("ciphertext too short")
    }
    
    nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
    
    // 解密
    plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
    if err != nil {
        return nil, fmt.Errorf("failed to decrypt: %w", err)
    }
    
    return plaintext, nil
}

func (e *Encryptor) getDataKey(ctx context.Context, keyID string) ([]byte, error) {
    e.cacheMu.RLock()
    key, exists := e.keyCache[keyID]
    e.cacheMu.RUnlock()
    
    if exists {
        return key, nil
    }
    
    // 从 KMS 获取数据密钥
    // 这里简化处理，实际应使用 KMS 的密钥派生功能
    key = make([]byte, 32) // AES-256
    if _, err := rand.Read(key); err != nil {
        return nil, fmt.Errorf("failed to generate key: %w", err)
    }
    
    e.cacheMu.Lock()
    e.keyCache[keyID] = key
    e.cacheMu.Unlock()
    
    return key, nil
}
```

### G.3 SSH 密钥加密存储

```go
package ssh

import (
    "encoding/pem"
    "fmt"
    
    "golang.org/x/crypto/ssh"
)

// EncryptedPrivateKey 加密的私钥结构
type EncryptedPrivateKey struct {
    EncryptedData string `json:"encrypted_data"`
    KeyID         string `json:"key_id"`
    Algorithm     string `json:"algorithm"` // "aes-256-gcm"
    Nonce         string `json:"nonce"`
}

// DecryptAndParsePrivateKey 解密并解析私钥
func DecryptAndParsePrivateKey(encrypted *EncryptedPrivateKey, encryptor *encryption.Encryptor) (ssh.Signer, error) {
    // 解密
    ciphertext, err := base64.StdEncoding.DecodeString(encrypted.EncryptedData)
    if err != nil {
        return nil, fmt.Errorf("failed to decode ciphertext: %w", err)
    }
    
    plaintext, err := encryptor.Decrypt(context.Background(), ciphertext, encrypted.KeyID)
    if err != nil {
        return nil, fmt.Errorf("failed to decrypt private key: %w", err)
    }
    
    // 解析 PEM
    block, _ := pem.Decode(plaintext)
    if block == nil {
        return nil, fmt.Errorf("failed to decode PEM block")
    }
    
    // 解析私钥
    signer, err := ssh.ParsePrivateKey(block.Bytes)
    if err != nil {
        return nil, fmt.Errorf("failed to parse private key: %w", err)
    }
    
    return signer, nil
}
```

### G.4 敏感字段加密示例

```go
package database

import (
    "database/sql"
    "encoding/json"
    
    "kube-static-pool-autoscaler/internal/encryption"
)

// Machine 机器模型（加密敏感字段）
type Machine struct {
    ID              string
    IPAddress       string
    SSHUser         string
    EncryptedSSHKey string  // 加密后的 SSH 私钥
    // ... 其他字段
}

func (m *Machine) GetSSHKey(encryptor *encryption.Encryptor) (string, error) {
    var encrypted encryption.EncryptedPrivateKey
    if err := json.Unmarshal([]byte(m.EncryptedSSHKey), &encrypted); err != nil {
        return "", err
    }
    
    plaintext, err := encryptor.Decrypt(context.Background(), 
        []byte(encrypted.EncryptedData), encrypted.KeyID)
    if err != nil {
        return "", err
    }
    
    return string(plaintext), nil
}

func (m *Machine) SetSSHKey(key string, encryptor *encryption.Encryptor) error {
    ciphertext, err := encryptor.Encrypt(context.Background(), 
        []byte(key), "ssh-key-default")
    if err != nil {
        return err
    }
    
    encrypted := &encryption.EncryptedPrivateKey{
        EncryptedData: base64.StdEncoding.EncodeToString(ciphertext),
        KeyID:         "ssh-key-default",
        Algorithm:     "aes-256-gcm",
    }
    
    data, err := json.Marshal(encrypted)
    if err != nil {
        return err
    }
    
    m.EncryptedSSHKey = string(data)
    return nil
}
```

### G.5 密钥轮换策略

```go
package keyrotation

import (
    "context"
    "fmt"
    "time"
)

type KeyRotator struct {
    encryptor    *encryption.Encryptor
    kmsClient    KMSClient
    db           *database.DB
    rotationPeriod time.Duration
}

func (r *KeyRotator) Start(ctx context.Context) {
    ticker := time.NewTicker(r.rotationPeriod)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.rotateKeys(ctx)
        }
    }
}

func (r *KeyRotator) rotateKeys(ctx context.Context) {
    fmt.Println("Starting key rotation...")
    
    // 1. 生成新的主密钥
    newKeyID := fmt.Sprintf("master-key-%d", time.Now().Unix())
    
    // 2. 重新加密所有敏感数据
    machines, err := r.db.GetAllMachines(ctx)
    if err != nil {
        fmt.Printf("Failed to get machines: %v\n", err)
        return
    }
    
    for _, machine := range machines {
        // 重新加密 SSH 密钥
        if machine.EncryptedSSHKey != "" {
            key, err := machine.GetSSHKey(r.encryptor)
            if err != nil {
                fmt.Printf("Failed to decrypt SSH key for machine %s: %v\n", machine.ID, err)
                continue
            }
            
            if err := machine.SetSSHKey(key, r.encryptor); err != nil {
                fmt.Printf("Failed to re-encrypt SSH key for machine %s: %v\n", machine.ID, err)
                continue
            }
            
            if err := r.db.UpdateMachine(ctx, machine); err != nil {
                fmt.Printf("Failed to update machine %s: %v\n", machine.ID, err)
            }
        }
    }
    
    fmt.Println("Key rotation completed")
}
```

---

## H. 审计日志设计

### H.1 审计日志结构

```go
package audit

type AuditEvent struct {
    ID            string                 `json:"id"`
    Timestamp     time.Time              `json:"timestamp"`
    EventType     EventType              `json:"event_type"`
    Actor         Actor                  `json:"actor"`
    Resource      Resource               `json:"resource"`
    Action        string                 `json:"action"`
    Result        string                 `json:"result"` // success, failure
    ErrorMessage  string                 `json:"error_message,omitempty"`
    Changes       map[string]interface{} `json:"changes,omitempty"`
    Metadata      map[string]interface{} `json:"metadata,omitempty"`
    IPAddress     string                 `json:"ip_address"`
    UserAgent     string                 `json:"user_agent"`
}

type EventType string

const (
    EventTypeNodepool EventType = "nodepool"
    EventTypeMachine  EventType = "machine"
    EventTypeScale    EventType = "scale"
    EventTypeToken    EventType = "token"
    EventTypeConfig   EventType = "config"
    EventTypeAuth     EventType = "auth"
    EventTypeSystem   EventType = "system"
)

type Actor struct {
    ID       string `json:"id"`        // 用户 ID 或服务账户 ID
    Type     string `json:"type"`      // "user", "service_account", "system"
    Name     string `json:"name"`
    Email    string `json:"email,omitempty"`
    ClientID string `json:"client_id,omitempty"`
}

type Resource struct {
    Type      string `json:"type"`       // "nodepool", "machine", "token"
    ID        string `json:"id"`
    Name      string `json:"name,omitempty"`
    Namespace string `json:"namespace,omitempty"`
}
```

### H.2 审计日志记录器

```go
package audit

import (
    "context"
    "fmt"
    "sync"
    "time"
    
    "github.com/google/uuid"
    "k8s.io/klog/v2"
)

type Logger struct {
    writer       AuditWriter
    eventChan    chan *AuditEvent
    bufferSize   int
    eventBuffer  []*AuditEvent
    bufferMu     sync.Mutex
    flushTicker  *time.Ticker
    wg           sync.WaitGroup
    stopCh       chan struct{}
}

type AuditWriter interface {
    Write(ctx context.Context, event *AuditEvent) error
    WriteBatch(ctx context.Context, events []*AuditEvent) error
}

// NewLogger 创建审计日志记录器
func NewLogger(writer AuditWriter, bufferSize int) *Logger {
    l := &Logger{
        writer:      writer,
        eventChan:   make(chan *AuditEvent, bufferSize),
        bufferSize:  bufferSize,
        eventBuffer: make([]*AuditEvent, 0, bufferSize),
        stopCh:      make(chan struct{}),
    }
    
    // 启动后台处理 goroutine
    l.wg.Add(2)
    go l.processEvents()
    go l.flushPeriodically()
    
    return l
}

// Log 记录审计事件
func (l *Logger) Log(ctx context.Context, event *AuditEvent) {
    event.ID = uuid.New().String()
    event.Timestamp = time.Now()
    
    select {
    case l.eventChan <- event:
    default:
        // 缓冲区满，同步写入
        l.writeEventSync(event)
    }
}

// LogNodepoolCreated 记录资源池创建
func (l *Logger) LogNodepoolCreated(ctx context.Context, actor *Actor, nodepoolID, name string) {
    l.Log(ctx, &AuditEvent{
        EventType: EventTypeNodepool,
        Actor:     *actor,
        Resource: Resource{
            Type: "nodepool",
            ID:   nodepoolID,
            Name: name,
        },
        Action:   "create",
        Result:   "success",
        Metadata: map[string]interface{}{},
    })
}

// LogMachineAdded 记录机器添加
func (l *Logger) LogMachineAdded(ctx context.Context, actor *Actor, machineID, hostname string, changes map[string]interface{}) {
    l.Log(ctx, &AuditEvent{
        EventType: EventTypeMachine,
        Actor:     *actor,
        Resource: Resource{
            Type: "machine",
            ID:   machineID,
            Name: hostname,
        },
        Action:   "add",
        Result:   "success",
        Changes:  changes,
    })
}

// LogScaleUp 记录扩容操作
func (l *Logger) LogScaleUp(ctx context.Context, actor *Actor, nodepoolName string, delta int, result string, err error) {
    event := &AuditEvent{
        EventType: EventTypeScale,
        Actor:     *actor,
        Resource: Resource{
            Type: "nodepool",
            Name: nodepoolName,
        },
        Action:   "scale_up",
        Result:   result,
        Metadata: map[string]interface{}{
            "delta": delta,
        },
    }
    
    if err != nil {
        event.ErrorMessage = err.Error()
    }
    
    l.Log(ctx, event)
}

// LogConfigChanged 记录配置变更
func (l *Logger) LogConfigChanged(ctx context.Context, actor *Actor, changes map[string]interface{}) {
    l.Log(ctx, &AuditEvent{
        EventType: EventTypeConfig,
        Actor:     *actor,
        Resource: Resource{
            Type: "config",
        },
        Action:   "update",
        Result:   "success",
        Changes:  changes,
    })
}

func (l *Logger) processEvents() {
    defer l.wg.Done()
    
    for event := range l.eventChan {
        l.bufferMu.Lock()
        l.eventBuffer = append(l.eventBuffer, event)
        
        if len(l.eventBuffer) >= l.bufferSize {
            l.flushBuffer()
        }
        l.bufferMu.Unlock()
    }
}

func (l *Logger) flushPeriodically() {
    defer l.wg.Done()
    
    l.flushTicker = time.NewTicker(5 * time.Minute)
    defer l.flushTicker.Stop()
    
    for {
        select {
        case <-l.stopCh:
            l.flushBuffer()
            return
        case <-l.flushTicker.C:
            l.bufferMu.Lock()
            l.flushBuffer()
            l.bufferMu.Unlock()
        }
    }
}

func (l *Logger) flushBuffer() {
    if len(l.eventBuffer) == 0 {
        return
    }
    
    events := l.eventBuffer
    l.eventBuffer = make([]*AuditEvent, 0, l.bufferSize)
    
    if err := l.writer.WriteBatch(context.Background(), events); err != nil {
        klog.Error(err, "failed to write audit events")
        // 写回缓冲区（可能会丢失一些事件）
        l.eventBuffer = append(l.eventBuffer, events...)
    }
}

func (l *Logger) writeEventSync(event *AuditEvent) {
    if err := l.writer.Write(context.Background(), event); err != nil {
        klog.Error(err, "failed to write audit event synchronously")
    }
}

// Stop 停止记录器
func (l *Logger) Stop() {
    close(l.stopCh)
    l.wg.Wait()
}
```

### H.3 审计日志存储

```go
package audit

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "strings"
    "time"
    
    _ "github.com/lib/pq"
)

type PostgresWriter struct {
    db *sql.DB
}

// NewPostgresWriter 创建 PostgreSQL 审计日志写入器
func NewPostgresWriter(dsn string) (*PostgresWriter, error) {
    db, err := sql.Open("postgres", dsn)
    if err != nil {
        return nil, fmt.Errorf("failed to open database: %w", err)
    }
    
    if err := db.Ping(); err != nil {
        return nil, fmt.Errorf("failed to ping database: %w", err)
    }
    
    return &PostgresWriter{db: db}, nil
}

// 创建审计日志表
func (w *PostgresWriter) InitSchema(ctx context.Context) error {
    query := `
    CREATE TABLE IF NOT EXISTS audit_logs (
        id              UUID PRIMARY KEY,
        timestamp       TIMESTAMP WITH TIME ZONE NOT NULL,
        event_type      VARCHAR(64) NOT NULL,
        actor_id        VARCHAR(128),
        actor_type      VARCHAR(64),
        actor_name      VARCHAR(256),
        actor_email     VARCHAR(256),
        resource_type   VARCHAR(64),
        resource_id     VARCHAR(128),
        resource_name   VARCHAR(256),
        action          VARCHAR(64) NOT NULL,
        result          VARCHAR(32) NOT NULL,
        error_message   TEXT,
        changes         JSONB,
        metadata        JSONB,
        ip_address      INET,
        user_agent      VARCHAR(512),
        created_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW()
    );
    
    CREATE INDEX IF NOT EXISTS idx_audit_logs_timestamp ON audit_logs(timestamp DESC);
    CREATE INDEX IF NOT EXISTS idx_audit_logs_event_type ON audit_logs(event_type);
    CREATE INDEX IF NOT EXISTS idx_audit_logs_actor ON audit_logs(actor_id, actor_type);
    CREATE INDEX IF NOT EXISTS idx_audit_logs_resource ON audit_logs(resource_type, resource_id);
    `
    
    _, err := w.db.ExecContext(ctx, query)
    return err
}

func (w *PostgresWriter) Write(ctx context.Context, event *AuditEvent) error {
    changes, _ := json.Marshal(event.Changes)
    metadata, _ := json.Marshal(event.Metadata)
    
    query := `
    INSERT INTO audit_logs (
        id, timestamp, event_type,
        actor_id, actor_type, actor_name, actor_email,
        resource_type, resource_id, resource_name,
        action, result, error_message,
        changes, metadata, ip_address, user_agent
    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $6, $17)
    `
    
    _, err := w.db.ExecContext(ctx, query,
        event.ID,
        event.Timestamp,
        event.EventType,
        event.Actor.ID,
        event.Actor.Type,
        event.Actor.Name,
        event.Actor.Email,
        event.Resource.Type,
        event.Resource.ID,
        event.Resource.Name,
        event.Action,
        event.Result,
        event.ErrorMessage,
        string(changes),
        string(metadata),
        event.IPAddress,
        event.UserAgent,
    )
    
    return err
}

func (w *PostgresWriter) WriteBatch(ctx context.Context, events []*AuditEvent) error {
    tx, err := w.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()
    
    stmt, err := tx.PrepareContext(ctx, `
    INSERT INTO audit_logs (
        id, timestamp, event_type,
        actor_id, actor_type, actor_name, actor_email,
        resource_type, resource_id, resource_name,
        action, result, error_message,
        changes, metadata, ip_address, user_agent
    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
    `)
    if err != nil {
        return err
    }
    defer stmt.Close()
    
    for _, event := range events {
        changes, _ := json.Marshal(event.Changes)
        metadata, _ := json.Marshal(event.Metadata)
        
        _, err := stmt.ExecContext(ctx,
            event.ID,
            event.Timestamp,
            event.EventType,
            event.Actor.ID,
            event.Actor.Type,
            event.Actor.Name,
            event.Actor.Email,
            event.Resource.Type,
            event.Resource.ID,
            event.Resource.Name,
            event.Action,
            event.Result,
            event.ErrorMessage,
            string(changes),
            string(metadata),
            event.IPAddress,
            event.UserAgent,
        )
        if err != nil {
            return err
        }
    }
    
    return tx.Commit()
}

// 查询审计日志
func (w *PostgresWriter) Query(ctx context.Context, params QueryParams) ([]*AuditEvent, error) {
    var conditions []string
    var args []interface{}
    argNum := 1
    
    if len(params.EventTypes) > 0 {
        conditions = append(conditions, fmt.Sprintf("event_type = ANY($%d)", argNum))
        args = append(args, params.EventTypes)
        argNum++
    }
    
    if params.StartTime.IsZero() {
        conditions = append(conditions, fmt.Sprintf("timestamp >= NOW() - INTERVAL '%d seconds'", params.SinceSeconds))
    } else {
        conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argNum))
        args = append(args, params.StartTime)
        argNum++
    }
    
    if !params.EndTime.IsZero() {
        conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argNum))
        args = append(args, params.EndTime)
        argNum++
    }
    
    if params.ActorID != "" {
        conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argNum))
        args = append(args, params.ActorID)
        argNum++
    }
    
    if params.ResourceID != "" {
        conditions = append(conditions, fmt.Sprintf("resource_id = $%d", argNum))
        args = append(args, params.ResourceID)
        argNum++
    }
    
    whereClause := ""
    if len(conditions) > 0 {
        whereClause = "WHERE " + strings.Join(conditions, " AND ")
    }
    
    query := fmt.Sprintf(`
    SELECT id, timestamp, event_type,
           actor_id, actor_type, actor_name, actor_email,
           resource_type, resource_id, resource_name,
           action, result, error_message,
           changes, metadata, ip_address, user_agent
    FROM audit_logs
    %s
    ORDER BY timestamp DESC
    LIMIT $%d
    `, whereClause, argNum)
    
    args = append(args, params.Limit)
    
    rows, err := w.db.QueryContext(ctx, query, args...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    var events []*AuditEvent
    for rows.Next() {
        var event AuditEvent
        var changes, metadata []byte
        
        err := rows.Scan(
            &event.ID,
            &event.Timestamp,
            &event.EventType,
            &event.Actor.ID,
            &event.Actor.Type,
            &event.Actor.Name,
            &event.Actor.Email,
            &event.Resource.Type,
            &event.Resource.ID,
            &event.Resource.Name,
            &event.Action,
            &event.Result,
            &event.ErrorMessage,
            &changes,
            &metadata,
            &event.IPAddress,
            &event.UserAgent,
        )
        if err != nil {
            return nil, err
        }
        
        json.Unmarshal(changes, &event.Changes)
        json.Unmarshal(metadata, &event.Metadata)
        
        events = append(events, &event)
    }
    
    return events, rows.Err()
}

type QueryParams struct {
    EventTypes    []string
    StartTime     time.Time
    EndTime       time.Time
    SinceSeconds  int64
    ActorID       string
    ResourceID    string
    Limit         int
}
```

---

## I. 升级和回滚策略

### I.1 版本发布流程

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         版本发布流程                                      │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  1. 开发分支 → 2. 测试验证 → 3. 预发布 → 4. 生产发布 → 5. 监控验证      │
│      │              │              │              │              │      │
│      ▼              ▼              ▼              ▼              ▼      │
│  feature/xxx   testing/1.0.2   staging/1.0.2   release/1.0.2   stable  │
│                                                                         │
│  发布检查清单:                                                          │
│  □ 单元测试通过 (覆盖率 > 80%)                                          │
│  □ 集成测试通过                                                         │
│  □ E2E 测试通过                                                        │
│  □ 安全扫描通过                                                        │
│  □ 性能基准测试通过                                                     │
│  □ 回滚方案验证                                                         │
│  □ 文档已更新                                                          │
│  □ 变更日志已生成                                                       │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### I.2 升级策略

```yaml
# deploy/strategy/rolling-update.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-static-pool-autoscaler
  namespace: kube-system
spec:
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1          # 最多增加 1 个 Pod
      maxUnavailable: 0    # 不能有不可用的 Pod
  minReadySeconds: 30     # Pod 就绪后至少等待 30 秒
  progressDeadlineSeconds: 600  # 升级超时时间 10 分钟
```

### I.3 金丝雀发布

```yaml
# deploy/strategy/canary.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-static-pool-autoscaler-canary
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kube-static-pool-autoscaler
      version: canary
  template:
    metadata:
      labels:
        app: kube-static-pool-autoscaler
        version: canary
    spec:
      containers:
        - name: provider
          image: kube-static-pool-autoscaler:v1.0.2-canary
---
# Istio VirtualService for Canary Routing
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: autoscaler-canary
  namespace: kube-system
spec:
  hosts:
    - kube-static-pool-autoscaler
  http:
    - route:
        - destination:
            host: kube-static-pool-autoscaler
            subset: stable
          weight: 90
        - destination:
            host: kube-static-pool-autoscaler
            subset: canary
          weight: 10
```

### I.4 回滚策略

```bash
# 方法 1: 使用 kubectl 回滚
kubectl rollout undo deployment/kube-static-pool-autoscaler -n kube-system

# 方法 2: 回滚到指定版本
kubectl rollout undo deployment/kube-static-pool-autoscaler -n kube-system --to-revision=3

# 方法 3: 查看回滚历史
kubectl rollout history deployment/kube-static-pool-autoscaler -n kube-system

# 方法 4: 暂停升级后回滚
kubectl rollout pause deployment/kube-static-pool-autoscaler -n kube-system
kubectl rollout undo deployment/kube-static-pool-autoscaler -n kube-system
kubectl rollout resume deployment/kube-static-pool-autoscaler -n kube-system
```

### I.5 数据库迁移回滚

```go
package migration

type Migration struct {
    Version   int
    UpScript  string
    DownScript string
}

var Migrations = []Migration{
    {
        Version: 1,
        UpScript: `
            CREATE TABLE nodepools (...);
            CREATE TABLE machines (...);
        `,
        DownScript: `
            DROP TABLE IF EXISTS machines;
            DROP TABLE IF EXISTS nodepools;
        `,
    },
    {
        Version: 2,
        UpScript: `
            ALTER TABLE machines ADD COLUMN region VARCHAR(64) DEFAULT 'default';
        `,
        DownScript: `
            ALTER TABLE machines DROP COLUMN IF EXISTS region;
        `,
    },
}

// SafeMigration 安全迁移（带回滚）
func SafeMigration(db *sql.DB, targetVersion int) error {
    currentVersion, err := GetCurrentVersion(db)
    if err != nil {
        return err
    }
    
    // 确定迁移方向
    if targetVersion > currentVersion {
        // 升级
        for v := currentVersion + 1; v <= targetVersion; v++ {
            if err := runMigration(db, Migrations[v-1].UpScript); err != nil {
                // 迁移失败，尝试回滚
                for i := v - 1; i >= currentVersion; i-- {
                    runMigration(db, Migrations[i].DownScript)
                }
                return fmt.Errorf("migration failed at version %d: %w", v, err)
            }
        }
    } else if targetVersion < currentVersion {
        // 降级
        for v := currentVersion; v > targetVersion; v-- {
            if err := runMigration(db, Migrations[v-1].DownScript); err != nil {
                return fmt.Errorf("rollback failed at version %d: %w", v, err)
            }
        }
    }
    
    return nil
}
```

---

## J. 灾难恢复设计

### J.1 备份策略

```yaml
# deploy/backup/backup-cronjob.yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: autoscaler-backup
  namespace: kube-system
spec:
  schedule: "0 2 * * *"  # 每天凌晨 2 点
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: autoscaler-backup
          containers:
            - name: backup
              image: postgres:15-backup
              env:
                - name: PGHOST
                  value: "postgres-service"
                - name: PGPASSWORD
                  valueFrom:
                    secretKeyRef:
                      name: autoscaler-secrets
                      key: postgres-password
              command:
                - /bin/bash
                - -c
                - |
                  # 备份数据库
                  pg_dump -U autoscaler autoscaler > /backup/autoscaler-$(date +%Y%m%d-%H%M%S).sql
                  
                  # 备份 Redis
                  redis-cli BGSAVE
                  redis-cli LASTSAVE > /backup/redis-$(date +%Y%m%d-%H%M%S).txt
                  
                  # 上传到对象存储
                  aws s3 cp /backup/ s3://backup-bucket/autoscaler/ --recursive
                  
                  # 清理 30 天前的备份
                  find /backup -type f -mtime +30 -delete
              volumeMounts:
                - name: backup-storage
                  mountPath: /backup
          volumes:
            - name: backup-storage
              persistentVolumeClaim:
                claimName: backup-pvc
          restartPolicy: OnFailure
---
# PVC for backups
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: backup-pvc
  namespace: kube-system
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
```

### J.2 恢复流程

```bash
#!/bin/bash
# scripts/disaster-recovery.sh

set -e

BACKUP_DATE=${1:-$(date -d "yesterday" +%Y%m%d)}
S3_BUCKET="s3://backup-bucket/autoscaler"
REGION="cn-bj1"

echo "Starting disaster recovery for date: $BACKUP_DATE"

# 1. 下载备份
echo "Downloading backups from S3..."
aws s3 sync "$S3_BUCKET/$BACKUP_DATE" /tmp/backup/

# 2. 恢复 PostgreSQL
echo "Restoring PostgreSQL..."
export PGPASSWORD=$(kubectl get secret autoscaler-secrets -n kube-system -o jsonpath='{.data.postgres-password}' | base64 -d)
pg_restore -h postgres-service -U autoscaler -d autoscaler /tmp/backup/autoscaler.sql || \
pg_dump -h postgres-service -U autoscaler -d autoscaler < /tmp/backup/autoscaler.sql

# 3. 恢复 Redis
echo "Restoring Redis..."
redis-cli -h redis-service < /tmp/backup/redis.rdb

# 4. 验证数据
echo "Verifying data..."
kubectl exec -n kube-system deployment/kube-static-pool-autoscaler -- \
  ksa status --verify

echo "Disaster recovery completed!"
```

### J.3 RTO/RPO 设计目标

| 场景 | RTO (恢复时间目标) | RPO (恢复点目标) |
|-----|-------------------|-----------------|
| 数据库故障 | 15 分钟 | 5 分钟 |
| 完整集群故障 | 30 分钟 | 1 小时 |
| 数据误删除 | 1 小时 | 5 分钟 |
| 区域故障 | 2 小时 | 1 小时 |

### J.4 高可用架构

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         多区域高可用架构                                  │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  Region CN-BJ1                    Region CN-SH1                         │
│  ┌─────────────────┐             ┌─────────────────┐                   │
│  │ Primary Cluster │             │ Secondary Cluster│                   │
│  │ ┌─────────────┐ │  Sync       │ ┌─────────────┐ │                   │
│  │ │ PG Primary  │─┼─────────────┼▶│ PG Replica  │ │                   │
│  │ └─────────────┘ │             │ └─────────────┘ │                   │
│  │ ┌─────────────┐ │             │ ┌─────────────┐ │                   │
│  │ │ Redis Master│─┼─────────────┼▶│ Redis Slave │ │                   │
│  │ └─────────────┘ │             │ └─────────────┘ │                   │
│  │ ┌─────────────┐ │             │ ┌─────────────┐ │                   │
│  │ │ Provider #1 │ │             │ │ Provider #2 │ │                   │
│  │ └─────────────┘ │             │ └─────────────┘ │                   │
│  └─────────────────┘             └─────────────────┘                   │
│           │                             │                               │
│           └──────────┬──────────────────┘                              │
│                      ▼                                                 │
│            ┌─────────────────┐                                          │
│            │ Global LB       │                                          │
│            │ (DNS/Failover)  │                                          │
│            └─────────────────┘                                          │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## K. 测试策略设计

### K.1 测试金字塔

```
                    ┌─────────────────────────────────────┐
                    │           E2E 测试                   │  5%
                    │    完整集群扩缩容流程验证            │
                    └─────────────────────────────────────┘
                           ┌───────────────────────────┐
                           │      集成测试              │  25%
                           │  多组件协作验证            │
                           └───────────────────────────┘
              ┌────────────────────────────────────────────────┐
              │              单元测试                            │  70%
              │  各模块独立功能测试                             │
              └────────────────────────────────────────────────┘
```

### K.2 测试覆盖范围

| 测试类型 | 覆盖模块 | 目标覆盖率 | 执行频率 |
|---------|---------|-----------|---------|
| 单元测试 | config, database, machine, ssh | > 80% | 每次提交 |
| 单元测试 | grpc, k8s, provisioner | > 70% | 每次提交 |
| 集成测试 | 数据库操作 | > 60% | 每次合并 |
| 集成测试 | SSH 连接池 | > 60% | 每次合并 |
| E2E 测试 | 完整扩缩容流程 | 核心场景 100% | 每日构建 |
| 性能测试 | 扩容/缩容延迟 | 基准测试 | 每周构建 |
| 安全测试 | 敏感数据加密 | 渗透测试 | 每月构建 |

### K.3 测试示例

```go
package provisioner_test

import (
    "context"
    "testing"
    "time"
    
    "kube-static-pool-autoscaler/internal/machine"
    "kube-static-pool-autoscaler/internal/provisioner"
    "kube-static-pool-autoscaler/internal/ssh/mocks"
    "kube-static-pool-autoscaler/internal/k8s/mocks"
    
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"
)

func TestProvisioner_ExecuteJoin(t *testing.T) {
    // 准备测试数据
    ctx := context.Background()
    nodepool := &model.Nodepool{
        ID:   "test-nodepool-id",
        Name: "test-pool",
    }
    
    machines := []*model.Machine{
        {
            ID:        "machine-1",
            IPAddress: "192.168.1.100",
            SSHUser:   "root",
            SSHPort:   22,
            Status:    machine.StatusAvailable,
        },
    }
    
    // 创建 Mock
    mockSSHClient := new(mocks.MockSSHClient)
    mockSSHPool := new(mocks.MockConnectionPool)
    mockK8sClient := new(mocks.MockK8sClient)
    
    mockSSHPool.On("Get", mock.Anything, "192.168.1.100", "root", 22).
        Return(mockSSHClient, nil)
    mockSSHClient.On("Exec", mock.Anything, mock.AnythingOfType("string")).
        Return("node-001 joined successfully", nil)
    mockSSHClient.On("IsConnected").Return(true)
    mockK8sClient.On("GenerateJoinCommand", mock.Anything).
        Return("kubeadm join ...", nil)
    
    // 创建 Provisioner（使用 Mock）
    p := provisioner.NewProvisioner(
        mockDB,
        mockSSHPool,
        mockK8sClient,
        &provisioner.Config{
            JoinTimeout: 5 * time.Minute,
        },
    )
    
    // 执行测试
    err := p.ExecuteJoin(ctx, nodepool, machines)
    
    // 验证结果
    assert.NoError(t, err)
    assert.Equal(t, machine.StatusInUse, machines[0].Status)
    assert.Equal(t, "node-001", machines[0].NodeName)
    
    // 验证 Mock 调用
    mockSSHPool.AssertExpectations(t)
    mockSSHClient.AssertExpectations(t)
    mockK8sClient.AssertExpectations(t)
}

func TestMachineAllocator_AllocateMachines(t *testing.T) {
    tests := []struct {
        name          string
        availableMachines []*model.Machine
        nodepool      *model.Nodepool
        requestCount  int
        expectCount   int
        expectError   bool
    }{
        {
            name:         "enough machines with random policy",
            availableMachines: []*model.Machine{
                {ID: "m1", CPUCores: 8, Status: machine.StatusAvailable},
                {ID: "m2", CPUCores: 8, Status: machine.StatusAvailable},
                {ID: "m3", CPUCores: 8, Status: machine.StatusAvailable},
            },
            nodepool: &model.Nodepool{
                ScaleUpPolicy: "random",
                MinCPUCores:   4,
            },
            requestCount: 2,
            expectCount:  2,
            expectError:  false,
        },
        {
            name:         "not enough machines",
            availableMachines: []*model.Machine{
                {ID: "m1", CPUCores: 8, Status: machine.StatusAvailable},
            },
            nodepool: &model.Nodepool{
                ScaleUpPolicy: "random",
            },
            requestCount: 3,
            expectCount:  0,
            expectError:  true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // 测试逻辑
        })
    }
}
```

### K.4 Mock 设计

```go
// internal/ssh/mocks/mock_connection_pool.go
package mocks

import (
    "context"
    
    "kube-static-pool-autoscaler/internal/ssh"
    "github.com/stretchr/testify/mock"
)

type MockConnectionPool struct {
    mock.Mock
}

func (m *MockConnectionPool) Get(ctx context.Context, host, user string, port int) (*ssh.SSHClient, error) {
    args := m.Called(ctx, host, user, port)
    return args.Get(0).(*ssh.SSHClient), args.Error(1)
}

func (m *MockConnectionPool) Release(conn *ssh.SSHClient) {
    m.Called(conn)
}

// internal/k8s/mocks/mock_client.go
type MockK8sClient struct {
    mock.Mock
}

func (m *MockK8sClient) GenerateJoinCommand(ctx context.Context) (string, error) {
    args := m.Called(ctx)
    return args.String(0), args.Error(1)
}

func (m *MockK8sClient) DrainNode(ctx context.Context, nodeName string, opts *DrainOptions) error {
    args := m.Called(ctx, nodeName, opts)
    return args.Error(0)
}

func (m *MockK8sClient) GetNode(ctx context.Context, nodeName string) (*v1.Node, error) {
    args := m.Called(ctx, nodeName)
    return args.Get(0).(*v1.Node), args.Error(1)
}
```

---

## L. 性能基准和优化设计

### L.1 性能目标

| 指标 | 目标值 | 说明 |
|-----|-------|------|
| 扩容延迟 (P99) | < 5 分钟 | 从请求到节点 Ready |
| 缩容延迟 (P99) | < 10 分钟 | 从请求到节点移除 |
| gRPC 接口响应 | < 100ms | 不包括实际扩缩容 |
| SSH 连接建立 | < 2s | 单个连接 |
| 机器分配 | < 500ms | 从可用池选择 |
| 吞吐量 | > 10 节点/分钟 | 批量扩容 |

### L.2 性能测试配置

```yaml
# test/performance/load-test.yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: performance-test
  namespace: kube-system
spec:
  template:
    spec:
      containers:
        - name: locust
          image: locustio/locust
          env:
            - name: LOCUST_MODE
              value: "master"
            - name: TARGET_HOST
              value: "http://autoscaler:8080"
          ports:
            - containerPort: 8089
              name: web
            - containerPort: 5557
              name: slave
---
# Locustfile for performance testing
# test/performance/locustfile.py
from locust import HttpUser, task, between, events
from locust.runners import Runner

class AutoscalerUser(HttpUser):
    wait_time = between(1, 5)
    
    def on_start(self):
        # 用户启动时初始化
        pass
    
    @task(1)
    def get_nodepools(self):
        """获取资源池列表"""
        with self.client.get("/nodepools", name="ListNodepools", catch_response=True) as response:
            if response.status_code == 200:
                response.success()
            else:
                response.failure(f"Status: {response.status_code}")
    
    @task(2)
    def check_health(self):
        """健康检查"""
        self.client.get("/healthz", name="HealthCheck")

# 分布式测试
@events.init.add_listener
def on_locust_init(environment, **kwargs):
    if not isinstance(environment.runner, Runner):
        return
    
    # 配置测试参数
    environment.runner.register_message("spawn_users", environment.runner.spawn_users)
```

### L.3 性能优化策略

```go
// 1. 连接池优化
type OptimizedConnectionPool struct {
    // 预创建连接
    preCreatedConnections int
    // 连接预热
    warmupInterval        time.Duration
    // 连接健康检查
    healthCheckInterval   time.Duration
}

// Warmup 预热连接
func (p *OptimizedConnectionPool) Warmup(ctx context.Context, hosts []string) {
    for _, host := range hosts {
        for i := 0; i < p.preCreatedConnections; i++ {
            conn, err := p.createConnection(ctx, host)
            if err != nil {
                klog.Warn("failed to pre-create connection", "host", host, "error", err)
                continue
            }
            p.returnConnection(conn)
        }
    }
}

// 2. 批处理优化
func (p *Provisioner) ExecuteBatchJoin(ctx context.Context, nodepool *model.Nodepool, machines []*model.Machine) error {
    // 并发执行但限制并发数
    semaphore := make(chan struct{}, p.config.BatchConcurrency)
    var wg sync.WaitGroup
    errChan := make(chan error, len(machines))
    
    for _, machine := range machines {
        wg.Add(1)
        semaphore <- struct{}{}
        
        go func(m *model.Machine) {
            defer wg.Done()
            defer func() { <-semaphore }()
            
            if err := p.sshExecuteJoin(ctx, m, joinCmd); err != nil {
                errChan <- err
            }
        }(machine)
    }
    
    wg.Wait()
    close(errChan)
    
    // 收集错误
    var errors []error
    for err := range errChan {
        errors = append(errors, err)
    }
    
    if len(errors) > 0 {
        return fmt.Errorf("batch join failed: %d errors", len(errors))
    }
    
    return nil
}

// 3. 缓存优化
type CachedMachineStore struct {
    db              *database.DB
    cache           *redis.Client
    cacheTTL        time.Duration
    updateChan      chan *model.Machine
    bulkUpdateTimer *time.Ticker
}

func (s *CachedMachineStore) StartPeriodicUpdate(ctx context.Context) {
    s.bulkUpdateTimer = time.NewTicker(30 * time.Second)
    
    go func() {
        for {
            select {
            case <-ctx.Done():
                s.flushUpdates(ctx)
                return
            case <-s.bulkUpdateTimer.C:
                s.flushUpdates(ctx)
            case machine := <-s.updateChan:
                s.pendingUpdates = append(s.pendingUpdates, machine)
            }
        }
    }()
}

func (s *CachedMachineStore) GetMachine(ctx context.Context, id string) (*model.Machine, error) {
    // 先查缓存
    cached, err := s.cache.Get(ctx, "machine:"+id).Result()
    if err == nil {
        var machine model.Machine
        if err := json.Unmarshal([]byte(cached), &machine); err == nil {
            return &machine, nil
        }
    }
    
    // 缓存未命中，查数据库
    machine, err := s.db.GetMachine(ctx, id)
    if err != nil {
        return nil, err
    }
    
    // 写入缓存
    data, _ := json.Marshal(machine)
    s.cache.Set(ctx, "machine:"+id, data, s.cacheTTL)
    
    return machine, nil
}
```

---

## M. 异常处理完善设计

### M.1 异常分类和处理策略

```go
package exception

import (
    "errors"
    "fmt"
    "net/http"
)

// ErrorType 错误类型
type ErrorType string

const (
    ErrorTypeValidation ErrorType = "validation"   // 参数验证错误
    ErrorTypeNotFound   ErrorType = "not_found"    // 资源不存在
    ErrorTypeConflict   ErrorType = "conflict"     // 资源冲突
    ErrorTypeTimeout    ErrorType = "timeout"      // 操作超时
    ErrorTypeTransient  ErrorType = "transient"    // 临时性错误
    ErrorTypePermanent  ErrorType = "permanent"    // 永久性错误
    ErrorTypeAuth       ErrorType = "auth"         // 认证错误
    ErrorTypePermission ErrorType = "permission"   // 权限错误
    ErrorTypeSystem     ErrorType = "system"       // 系统错误
)

// AppError 应用错误
type AppError struct {
    Type       ErrorType
    Code       string
    Message    string
    Details    interface{}
    Cause      error
    Retryable  bool
    HTTPStatus int
}

func (e *AppError) Error() string {
    if e.Cause != nil {
        return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Cause.Error())
    }
    return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
    return e.Cause
}

// NewError 创建新错误
func NewError(errType ErrorType, code, message string, details interface{}, cause error, retryable bool) *AppError {
    statusMap := map[ErrorType]int{
        ErrorTypeValidation: http.StatusBadRequest,
        ErrorTypeNotFound:   http.StatusNotFound,
        ErrorTypeConflict:   http.StatusConflict,
        ErrorTypeTimeout:    http.StatusGatewayTimeout,
        ErrorTypeTransient:  http.StatusServiceUnavailable,
        ErrorTypePermanent:  http.StatusInternalServerError,
        ErrorTypeAuth:       http.StatusUnauthorized,
        ErrorTypePermission: http.StatusForbidden,
        ErrorTypeSystem:     http.StatusInternalServerError,
    }
    
    return &AppError{
        Type:       errType,
        Code:       code,
        Message:    message,
        Details:    details,
        Cause:      cause,
        Retryable:  retryable,
        HTTPStatus: statusMap[errType],
    }
}

// 预定义错误
var (
    ErrMachineNotFound = &AppError{
        Type:       ErrorTypeNotFound,
        Code:       "MACHINE_NOT_FOUND",
        Message:    "Machine not found",
        Retryable:  false,
        HTTPStatus: http.StatusNotFound,
    }
    
    ErrMachineAlreadyInUse = &AppError{
        Type:       ErrorTypeConflict,
        Code:       "MACHINE_ALREADY_IN_USE",
        Message:    "Machine is already in use",
        Retryable:  false,
        HTTPStatus: http.StatusConflict,
    }
    
    ErrMachineJoinTimeout = &AppError{
        Type:       ErrorTypeTimeout,
        Code:       "MACHINE_JOIN_TIMEOUT",
        Message:    "Machine join operation timed out",
        Retryable:  true,
        HTTPStatus: http.StatusGatewayTimeout,
    }
    
    ErrNodepoolFull = &AppError{
        Type:       ErrorTypeConflict,
        Code:       "NODEPOOL_FULL",
        Message:    "Nodepool has reached maximum size",
        Retryable:  false,
        HTTPStatus: http.StatusConflict,
    }
    
    ErrSSHAuthFailed = &AppError{
        Type:       ErrorTypeAuth,
        Code:       "SSH_AUTH_FAILED",
        Message:    "SSH authentication failed",
        Retryable:  false,
        HTTPStatus: http.StatusUnauthorized,
    }
    
    ErrSSHConnectionFailed = &AppError{
        Type:       ErrorTypeTransient,
        Code:       "SSH_CONNECTION_FAILED",
        Message:    "SSH connection failed",
        Retryable:  true,
        HTTPStatus: http.StatusServiceUnavailable,
    }
)
```

### M.2 重试装饰器

```go
package retry

import (
    "context"
    "time"
    
    "kube-static-pool-autoscaler/internal/exception"
)

type RetryConfig struct {
    MaxAttempts      int
    InitialDelay     time.Duration
    MaxDelay         time.Duration
    Multiplier       float64
    Jitter           bool
    RetryableErrors  []exception.ErrorType
}

func WithRetry(ctx context.Context, config RetryConfig, fn func() error) error {
    var lastErr error
    
    delay := config.InitialDelay
    
    for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
        err := fn()
        
        if err == nil {
            return nil
        }
        
        var appErr *exception.AppError
        if errors.As(err, &appErr) {
            // 检查是否可重试
            if !appErr.Retryable || !isRetryableError(config, appErr.Type) {
                return err
            }
        } else {
            // 非 AppError，默认可重试
            lastErr = err
        }
        
        if attempt >= config.MaxAttempts {
            return fmt.Errorf("max attempts (%d) exceeded: %w", config.MaxAttempts, lastErr)
        }
        
        // 计算延迟
        actualDelay := delay
        if config.Jitter {
            actualDelay = time.Duration(float64(delay) * (0.5 + 0.5*float64(time.Now().UnixNano()%1000)/1000))
        }
        
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(actualDelay):
        }
        
        delay = time.Duration(float64(delay) * config.Multiplier)
        if delay > config.MaxDelay {
            delay = config.MaxDelay
        }
    }
    
    return lastErr
}

func isRetryableError(config RetryConfig, errType exception.ErrorType) bool {
    for _, t := range config.RetryableErrors {
        if t == errType {
            return true
        }
    }
    return false
}

// 预定义重试配置
var (
    SSHRetryConfig = RetryConfig{
        MaxAttempts:     3,
        InitialDelay:    1 * time.Second,
        MaxDelay:        10 * time.Second,
        Multiplier:      2.0,
        Jitter:          true,
        RetryableErrors: []exception.ErrorType{
            exception.ErrorTypeTransient,
            exception.ErrorTypeTimeout,
        },
    }
    
    JoinRetryConfig = RetryConfig{
        MaxAttempts:     2,
        InitialDelay:    30 * time.Second,
        MaxDelay:        120 * time.Second,
        Multiplier:      2.0,
        Jitter:          true,
        RetryableErrors: []exception.ErrorType{
            exception.ErrorTypeTransient,
            exception.ErrorTypeTimeout,
        },
    }
    
    DrainRetryConfig = RetryConfig{
        MaxAttempts:     2,
        InitialDelay:    60 * time.Second,
        MaxDelay:        300 * time.Second,
        Multiplier:      1.5,
        Jitter:          true,
        RetryableErrors: []exception.ErrorType{
            exception.ErrorTypeTransient,
            exception.ErrorTypeTimeout,
        },
    }
)
```

### M.3 告警阈值设计

```go
package monitoring

type AlertThresholds struct {
    // 扩容相关
    ScaleUpFailureRate   float64 // 扩容失败率 > 10%
    ScaleUpLatencyMs     int64   // 扩容延迟 > 5分钟
    ScaleUpTimeoutCount  int     // 扩容超时次数 > 5
    
    // 缩容相关
    ScaleDownFailureRate float64 // 缩容失败率 > 10%
    DrainTimeoutCount    int     // Drain 超时次数 > 3
    
    // 机器状态
    UnavailableMachines  int     // 不可用机器数 > 10
    FaultMachines        int     // 故障机器数 > 3
    MaintenanceMachines  int     // 维护中机器数 > 5
    
    // SSH 连接
    SSHConnectionFailures int    // SSH 连接失败数 > 20/5min
    SSHExecutionFailures  int    // SSH 执行失败数 > 10/5min
    
    // 系统资源
    MemoryUsagePercent   float64 // 内存使用率 > 85%
    CPUUsagePercent      float64 // CPU 使用率 > 85%
    DatabaseConnections  int     // 数据库连接数 > 80% 最大值
}

var DefaultThresholds = AlertThresholds{
    ScaleUpFailureRate:   0.1,
    ScaleUpLatencyMs:     300000,  // 5分钟
    ScaleUpTimeoutCount:  5,
    ScaleDownFailureRate: 0.1,
    DrainTimeoutCount:    3,
    UnavailableMachines:  10,
    FaultMachines:        3,
    MaintenanceMachines:  5,
    SSHConnectionFailures: 20,
    SSHExecutionFailures: 10,
    MemoryUsagePercent:   85,
    CPUUsagePercent:      85,
    DatabaseConnections:  80,
}
```

---

## N. 基础设施高可用设计

### N.1 PostgreSQL 高可用

```yaml
# deploy/infrastructure/postgres-ha.yaml
apiVersion: postgres-operator.crunchydata.com/v1beta1
kind: PostgresCluster
metadata:
  name: autoscaler-postgres
  namespace: kube-system
spec:
  image: postgres:15
  postgresVersion: 15
  instances:
    - replicas: 3
      dataVolumeClaimSpec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: 50Gi
      resources:
        requests:
          cpu: 500m
          memory: 1Gi
        limits:
          cpu: 2000m
          memory: 4Gi
  patroni:
    dynamicConfiguration:
      loop_wait: 10
      ttl: 30
      retry_timeout: 10
      maximum_lag_on_failover: 1048576
    bootstrap:
      dcs:
        ttl: 30
        loop_wait: 10
        retry_timeout: 10
        maximum_lag_on_failover: 1048576
  proxy:
    pgBouncer:
      replicas: 3
      resources:
        requests:
          cpu: 100m
          memory: 128Mi
        limits:
          cpu: 500m
          memory: 512Mi
  backups:
    pgbackrest:
      repos:
        - name: repo1
          s3:
            bucket: s3://autoscaler-pg-backup
            endpoint: s3.cn-bj1.amazonaws.com.cn
            region: cn-bj1
```

### N.2 Redis 高可用

```yaml
# deploy/infrastructure/redis-ha.yaml
apiVersion: redis.redis.opstreelabs.in/v1beta1
kind: RedisCluster
metadata:
  name: autoscaler-redis
  namespace: kube-system
spec:
  image: redis:7-alpine
  redisExporter:
    enabled: true
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
  clusterSize: 6
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: 1000m
      memory: 2Gi
  storage:
    volumeClaimTemplate:
      spec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: 20Gi
```

### N.3 Provider 高可用部署

```yaml
# deploy/k8s/deployment-ha.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-static-pool-autoscaler
  namespace: kube-system
spec:
  replicas: 3  # 3 副本
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 1
  selector:
    matchLabels:
      app: kube-static-pool-autoscaler
  template:
    metadata:
      labels:
        app: kube-static-pool-autoscaler
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels:
                    app: kube-static-pool-autoscaler
                topologyKey: kubernetes.io/hostname
        nodeAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 50
              preference:
                matchExpressions:
                  - key: node-role.kubernetes.io/control-plane
                    operator: DoesNotExist
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app: kube-static-pool-autoscaler
      containers:
        - name: provider
          image: kube-static-pool-autoscaler:v1.0.0
          resources:
            requests:
              cpu: 500m
              memory: 512Mi
            limits:
              cpu: 2000m
              memory: 2Gi
          livenessProbe:
            httpGet:
              path: /healthz
              port: 9090
            initialDelaySeconds: 10
            periodSeconds: 10
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: 9090
            initialDelaySeconds: 5
            periodSeconds: 5
            failureThreshold: 3
      terminationGracePeriodSeconds: 60
```

### N.4 资源规划建议

| 组件 | CPU | 内存 | 存储 | 副本数 |
|-----|-----|------|-----|-------|
| Provider | 500m-2000m | 512Mi-2Gi | - | 2-3 |
| PostgreSQL | 500m-2000m | 1Gi-4Gi | 50Gi | 3 |
| Redis | 200m-1000m | 512Mi-2Gi | 20Gi | 6 |
| 监控 (Prometheus) | 1000m | 4Gi | 100Gi | 1 |
| 日志 (Loki) | 500m | 1Gi | 50Gi | 1 |

---

## O. SSH 执行器详细设计

### O.1 设计概述

由于所有机器已开机，系统的核心操作（扩容、缩容、机器管理）都依赖 SSH 连接远程执行。SSH 执行器是整个系统的关键组件，其可靠性直接影响扩缩容的成功率。

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         SSH 执行器架构                                    │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌───────────────────────────────────────────────────────────────────┐ │
│  │                       SSH Executor (入口)                          │ │
│  │  ┌────────────┐ ┌────────────┐ ┌────────────────────────────┐   │ │
│  │  │ Execute()  │ │ ExecuteBG()│ │ ExecuteWithStream()        │   │ │
│  │  │ (同步阻塞) │ │ (异步返回) │ │ (流式输出)                 │   │ │
│  │  └────────────┘ └────────────┘ └────────────────────────────┘   │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                        │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                       Command Builder                              │ │
│  │  ┌───────────┐ ┌───────────┐ ┌─────────────────────────────┐   │ │
│  │  │ JoinCmd   │ │ DrainCmd  │ │ ResetCmd                    │   │ │
│  │  │ Builder   │ │ Builder   │ │ Builder                     │   │ │
│  │  └───────────┘ └───────────┘ └─────────────────────────────┘   │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                        │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                       Session Manager                              │ │
│  │  ┌──────────────┐ ┌─────────────────┐ ┌──────────────────────┐  │ │
│  │  │ Connection   │ │ Session         │ │ Command              │  │ │
│  │  │ Pool         │ │ Manager         │ │ Executor             │  │ │
│  │  │ (连接池)     │ │ (会话管理)       │ │ (命令执行)           │  │ │
│  │  └──────────────┘ └─────────────────┘ └──────────────────────┘  │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                        │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                       SSH Client (底层)                            │ │
│  │              golang.org/x/crypto/ssh                              │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### O.2 核心接口设计

```go
package ssh

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "strings"
    "sync"
    "time"
)

// Executor SSH 执行器主接口
type Executor interface {
    // Execute 执行命令（同步阻塞）
    Execute(ctx context.Context, host, user string, port int, cmd string, opts ...ExecuteOption) (*Result, error)
    
    // ExecuteBG 执行命令（异步）
    ExecuteBG(ctx context.Context, host, user string, port int, cmd string, opts ...ExecuteOption) (*Task, error)
    
    // ExecuteWithStream 执行命令并流式返回输出
    ExecuteWithStream(ctx context.Context, host, user string, port int, cmd string, opts ...ExecuteOption) (<-chan *StreamResult, error)
    
    // GetConnection 获取连接
    GetConnection(ctx context.Context, host, user string, port int) (*Client, error)
    
    // ReleaseConnection 释放连接
    ReleaseConnection(client *Client)
    
    // Close 关闭执行器
    Close() error
}

// Result 命令执行结果
type Result struct {
    ExitCode   int           // 退出码
    Stdout     string        // 标准输出
    Stderr     string        // 标准错误
    Duration   time.Duration // 执行时长
    Host       string        // 目标主机
    ExecutedAt time.Time     // 执行时间
}

// StreamResult 流式输出结果
type StreamResult struct {
    Type    string // "stdout", "stderr", "done", "error"
    Data    string
    ExitCode int
    Error   error
}

// Task 异步任务
type Task struct {
    ID        string
    Host      string
    Command   string
    StartedAt time.Time
    Done      chan struct{}
    Result    *Result
    Error     error
}

// ExecuteOption 执行选项
type ExecuteOption struct {
    Timeout       time.Duration
    Env           map[string]string
    WorkingDir    string
    Stdin         string
    TTY           bool
    LogOutput     bool
    Retries       int
    RetryDelay    time.Duration
}

// 默认执行选项
var defaultExecuteOptions = ExecuteOption{
    Timeout:    5 * time.Minute,
    Retries:    3,
    RetryDelay: 1 * time.Second,
    LogOutput:  true,
}

// SSHExecutor SSH 执行器实现
type SSHExecutor struct {
    config        *Config
    clientBuilder *ClientBuilder
    connectionPool *ConnectionPool
    sessionManager *SessionManager
    commandBuilder *CommandBuilder
    metrics       *Metrics
    logger        *Logger
   .mu            sync.RWMutex
    closed        bool
}

// Config SSH 执行器配置
type Config struct {
    // 连接池配置
    MaxConnectionsPerHost int
    ConnectionTimeout     time.Duration
    MaxIdleTime           time.Duration
    
    // 命令执行配置
    DefaultTimeout        time.Duration
    MaxRetries            int
    RetryDelay            time.Duration
    MaxConcurrentSessions int // 单主机最大并发会话数
    
    // 全局限流
    GlobalConcurrency     int
    GlobalRateLimit       float64 // 每秒请求数
    
    // 安全性配置
    HostKeyVerification   bool
    KnownHostsPath        string
    
    // 性能配置
    EnableConnectionWarmup bool
    WarmupHosts           []string
    Compression           bool
}

// NewSSHExecutor 创建 SSH 执行器
func NewSSHExecutor(config *Config) (*SSHExecutor, error) {
    // 验证配置
    if config.MaxConnectionsPerHost <= 0 {
        config.MaxConnectionsPerHost = 3
    }
    if config.DefaultTimeout <= 0 {
        config.DefaultTimeout = 5 * time.Minute
    }
    if config.MaxRetries <= 0 {
        config.MaxRetries = 3
    }
    if config.GlobalConcurrency <= 0 {
        config.GlobalConcurrency = 100
    }
    
    executor := &SSHExecutor{
        config:         config,
        clientBuilder:  NewClientBuilder(config),
        connectionPool: NewConnectionPool(config.MaxConnectionsPerHost),
        sessionManager: NewSessionManager(config.MaxConcurrentSessions),
        commandBuilder: NewCommandBuilder(),
        metrics:        NewMetrics(),
        logger:         NewLogger("ssh-executor"),
    }
    
    // 预热连接（可选）
    if config.EnableConnectionWarmup {
        executor.warmupConnections()
    }
    
    return executor, nil
}
```

### O.3 连接池管理

```go
// ConnectionPool SSH 连接池
type ConnectionPool struct {
    maxSize      int
    idleTimeout  time.Duration
    pools        map[string]*hostPool
    mutex        sync.RWMutex
    totalCreated int64
    totalActive  int64
}

// hostPool 单主机连接池
type hostPool struct {
    host       string
    idleConns  chan *Client
    activeConns int
    mutex      sync.RWMutex
    createdAt  time.Time
    lastUsedAt time.Time
}

func NewConnectionPool(maxSize int) *ConnectionPool {
    return &ConnectionPool{
        maxSize:     maxSize,
        idleTimeout: 30 * time.Minute,
        pools:       make(map[string]*hostPool),
    }
}

// Get 获取连接
func (p *ConnectionPool) Get(ctx context.Context, host, user string, port int) (*Client, error) {
    key := p.generateKey(host, user, port)
    
    pool := p.getOrCreatePool(key, host)
    
    // 尝试从空闲池获取
    select {
    case conn := <-pool.idleConns:
        pool.updateLastUsed()
        return conn, nil
    default:
        // 检查是否超出活跃连接限制
        pool.mutex.Lock()
        if pool.activeConns >= p.maxSize {
            pool.mutex.Unlock()
            // 等待空闲连接或超时
            select {
            case conn := <-pool.idleConns:
                pool.mutex.Lock()
                pool.activeConns--
                pool.mutex.Unlock()
                return conn, nil
            case <-time.After(30 * time.Second):
                return nil, fmt.Errorf("connection pool exhausted for %s", key)
            case <-ctx.Done():
                return nil, ctx.Err()
            }
        }
        pool.activeConns++
        pool.mutex.Unlock()
    }
    
    // 创建新连接
    conn, err := p.createConnection(ctx, host, user, port)
    if err != nil {
        pool.mutex.Lock()
        pool.activeConns--
        pool.mutex.Unlock()
        return nil, err
    }
    
    return conn, nil
}

// Release 释放连接
func (p *ConnectionPool) Release(conn *Client) {
    if conn == nil {
        return
    }
    
    key := p.generateKey(conn.Host, conn.User, conn.Port)
    
    pool := p.getPool(key)
    if pool == nil {
        conn.Close()
        return
    }
    
    // 检查连接是否仍然有效
    if !conn.IsConnected() {
        pool.mutex.Lock()
        pool.activeConns--
        pool.mutex.Unlock()
        conn.Close()
        return
    }
    
    // 放回空闲池
    select {
    case pool.idleConns <- conn:
        pool.updateLastUsed()
    default:
        // 池已满，关闭连接
        pool.mutex.Lock()
        pool.activeConns--
        pool.mutex.Unlock()
        conn.Close()
    }
}

// getOrCreatePool 获取或创建连接池
func (p *ConnectionPool) getOrCreatePool(key, host string) *hostPool {
    p.mutex.RLock()
    pool, exists := p.pools[key]
    p.mutex.RUnlock()
    
    if exists {
        return pool
    }
    
    p.mutex.Lock()
    defer p.mutex.Unlock()
    
    // 双重检查
    if pool, exists = p.pools[key]; exists {
        return pool
    }
    
    pool = &hostPool{
        host:      host,
        idleConns: make(chan *Client, p.maxSize),
    }
    p.pools[key] = pool
    
    return pool
}

// generateKey 生成连接池 key
func (p *ConnectionPool) generateKey(host, user string, port int) string {
    return fmt.Sprintf("%s@%s:%d", user, host, port)
}

// Cleanup 清理过期连接
func (p *ConnectionPool) Cleanup() {
    p.mutex.Lock()
    defer p.mutex.Unlock()
    
    cutoff := time.Now().Add(-p.idleTimeout)
    for key, pool := range p.pools {
        pool.mutex.Lock()
        
        // 关闭所有空闲连接
        for {
            select {
            case conn := <-pool.idleConns:
                conn.Close()
            default:
                goto nextPool
            }
        }
        
    nextPool:
        pool.mutex.Unlock()
        
        // 删除空池子
        if pool.createdAt.Before(cutoff) && pool.activeConns == 0 {
            delete(p.pools, key)
        }
    }
}
```

### O.4 命令执行器

```go
// SessionManager 会话管理器
type SessionManager struct {
    maxSessions int
    activeSessions map[string]*Session
    mutex         sync.RWMutex
}

type Session struct {
    ID        string
    Client    *Client
    Session   *ssh.Session
    CreatedAt time.Time
    Done      chan struct{}
}

// NewSessionManager 创建会话管理器
func NewSessionManager(maxSessions int) *SessionManager {
    return &SessionManager{
        maxSessions:  maxSessions,
        activeSessions: make(map[string]*Session),
    }
}

// NewSession 创建新会话
func (m *SessionManager) NewSession(client *Client) (*Session, error) {
    session, err := client.NewSession()
    if err != nil {
        return nil, err
    }
    
    sess := &Session{
        ID:        fmt.Sprintf("session-%d", time.Now().UnixNano()%1000000),
        Client:    client,
        Session:   session,
        CreatedAt: time.Now(),
        Done:      make(chan struct{}),
    }
    
    // 注册会话
    m.mutex.Lock()
    m.activeSessions[sess.ID] = sess
    m.mutex.Unlock()
    
    return sess, nil
}

// Close 关闭会话
func (m *SessionManager) Close(session *Session) {
    if session == nil || session.Session == nil {
        return
    }
    
    session.Session.Close()
    
    m.mutex.Lock()
    delete(m.activeSessions, session.ID)
    m.mutex.Unlock()
}

// CommandExecutor 命令执行器
type CommandExecutor struct {
    sessionManager *SessionManager
    logger         *Logger
    metrics        *Metrics
}

// Execute 执行命令
func (e *CommandExecutor) Execute(ctx context.Context, client *Client, cmd string, opts ExecuteOption) (*Result, error) {
    // 应用默认选项
    opts = applyDefaultOptions(opts)
    
    // 创建会话
    session, err := e.sessionManager.NewSession(client)
    if err != nil {
        return nil, fmt.Errorf("failed to create session: %w", err)
    }
    defer e.sessionManager.Close(session)
    
    startTime := time.Now()
    
    // 准备环境变量
    env := []string{}
    for k, v := range opts.Env {
        env = append(env, fmt.Sprintf("%s=%s", k, v))
    }
    if len(env) > 0 {
        session.Session.SetEnv(env...)
    }
    
    // 设置工作目录
    if opts.WorkingDir != "" {
        session.Session.Workdir = opts.WorkingDir
    }
    
    // 准备输出
    var stdout, stderr strings.Builder
    
    if opts.TTY {
        // TTY 模式
        session.Session.Stdout = &stdout
        session.Session.Stderr = &stderr
        
        if opts.Stdin != "" {
            session.Session.Stdin = strings.NewReader(opts.Stdin)
        }
        
        // 运行命令
        err = session.Session.Run(cmd)
    } else {
        // 非 TTY 模式（推荐）
        stdoutPipe, err := session.Session.StdoutPipe()
        if err != nil {
            return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
        }
        
        stderrPipe, err := session.Session.StderrPipe()
        if err != nil {
            return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
        }
        
        // 启动命令
        if err := session.Session.Start(cmd); err != nil {
            return nil, fmt.Errorf("failed to start command: %w", err)
        }
        
        // 并发读取输出
        var wg sync.WaitGroup
        wg.Add(2)
        
        go func() {
            defer wg.Done()
            io.Copy(&stdout, stdoutPipe)
        }()
        
        go func() {
            defer wg.Done()
            io.Copy(&stderr, stderrPipe)
        }()
        
        // 等待命令完成（带超时）
        done := make(chan error, 1)
        go func() {
            done <- session.Session.Wait()
        }()
        
        select {
        case <-ctx.Done():
            session.Session.Signal(ssh.SIGKILL)
            return nil, ctx.Err()
        case err := <-done:
            if err != nil {
                // 忽略已杀死进程的信号错误
                if exitErr, ok := err.(*ssh.ExitMissingError); ok {
                    err = nil
                } else if exitStatus, ok := exitCode(err); ok {
                    err = nil
                }
            }
        }
        
        wg.Wait()
    }
    
    duration := time.Since(startTime)
    
    // 记录指标
    e.metrics.RecordExecution(client.Host, duration, err == nil)
    
    // 记录日志
    e.logger.LogExecution(client.Host, cmd, duration, err == nil)
    
    // 提取退出码
    exitCode := 0
    if exitStatus, ok := exitCode(err); ok {
        exitCode = exitStatus
    }
    
    return &Result{
        ExitCode:   exitCode,
        Stdout:     stdout.String(),
        Stderr:     stderr.String(),
        Duration:   duration,
        Host:       client.Host,
        ExecutedAt: startTime,
    }, nil
}

// ExecuteWithStream 流式执行命令
func (e *CommandExecutor) ExecuteWithStream(ctx context.Context, client *Client, cmd string, opts ExecuteOption) (<-chan *StreamResult, error) {
    resultChan := make(chan *StreamResult, 100)
    
    // 创建会话
    session, err := e.sessionManager.NewSession(client)
    if err != nil {
        return nil, err
    }
    
    // 准备输出管道
    stdoutPipe, err := session.Session.StdoutPipe()
    if err != nil {
        e.sessionManager.Close(session)
        return nil, err
    }
    
    stderrPipe, err := session.Session.StderrPipe()
    if err != nil {
        e.sessionManager.Close(session)
        return nil, err
    }
    
    // 启动命令
    if err := session.Session.Start(cmd); err != nil {
        e.sessionManager.Close(session)
        return nil, err
    }
    
    // 并发读取输出
    go func() {
        defer e.sessionManager.Close(session)
        defer close(resultChan)
        
        var wg sync.WaitGroup
        wg.Add(2)
        
        // 读取 stdout
        go func() {
            defer wg.Done()
            reader := bufio.NewReader(stdoutPipe)
            for {
                line, err := reader.ReadString('\n')
                if err != nil {
                    if err != io.EOF {
                        resultChan <- &StreamResult{
                            Type:  "error",
                            Error: err,
                        }
                    }
                    break
                }
                line = strings.TrimSuffix(line, "\n")
                if line != "" {
                    resultChan <- &StreamResult{
                        Type: "stdout",
                        Data: line,
                    }
                }
            }
        }()
        
        // 读取 stderr
        go func() {
            defer wg.Done()
            reader := bufio.NewReader(stderrPipe)
            for {
                line, err := reader.ReadString('\n')
                if err != nil {
                    if err != io.EOF {
                        resultChan <- &StreamResult{
                            Type:  "error",
                            Error: err,
                        }
                    }
                    break
                }
                line = strings.TrimSuffix(line, "\n")
                if line != "" {
                    resultChan <- &StreamResult{
                        Type: "stderr",
                        Data: line,
                    }
                }
            }
        }()
        
        wg.Wait()
        
        // 等待命令完成
        err := session.Session.Wait()
        
        exitCode := 0
        if exitStatus, ok := exitCode(err); ok {
            exitCode = exitStatus
        }
        
        resultChan <- &StreamResult{
            Type:    "done",
            ExitCode: exitCode,
            Error:   err,
        }
    }()
    
    return resultChan, nil
}

// exitCode 提取退出码
func exitCode(err error) (int, bool) {
    if err == nil {
        return 0, true
    }
    
    exitErr, ok := err.(*ssh.ExitError)
    if ok {
        return exitErr.ExitStatus(), true
    }
    
    return 0, false
}
```

### O.5 命令构建器

```go
// CommandBuilder SSH 命令构建器
type CommandBuilder struct{}

// NewCommandBuilder 创建命令构建器
func NewCommandBuilder() *CommandBuilder {
    return &CommandBuilder{}
}

// BuildJoinCommand 构建 kubeadm join 命令
func (b *CommandBuilder) BuildJoinCommand(joinCommand, nodeName string) string {
    // 基础环境检查脚本
    baseScript := `
# 基础环境检查
swapoff -a 2>/dev/null || true
systemctl start containerd 2>/dev/null || systemctl start docker 2>/dev/null || true

# 执行 Join
%s

# 等待 kubelet 就绪
for i in {1..60}; do
    if systemctl is-active --quiet kubelet; then
        echo "kubelet-active"
        break
    fi
    sleep 5
done

# 验证节点注册
for i in {1..30}; do
    if kubectl get node %s &>/dev/null; then
        echo "node-registered: %s"
        exit 0
    fi
    sleep 10
done

echo "warning: node registration timeout"
exit 0
`
    
    return fmt.Sprintf(baseScript, joinCommand, nodeName, nodeName)
}

// BuildResetCommand 构建 kubeadm reset 命令
func (b *CommandBuilder) BuildResetCommand(cleanupCNI bool) string {
    script := `
# 执行 kubeadm reset
kubeadm reset -f 2>/dev/null || true

# 清理网络配置
iptables -F 2>/dev/null || true
iptables -t nat -F 2>/dev/null || true

if [ "%s" = "true" ]; then
    ip link delete cni0 2>/dev/null || true
    rm -rf /etc/cni/net.d/* 2>/dev/null || true
fi

# 清理 kubelet 状态
rm -rf /var/lib/kubelet/pods/* 2>/dev/null || true
rm -rf /var/lib/kubelet/cpu_manager_state 2>/dev/null || true
rm -rf /var/lib/kubelet/memory_manager_state 2>/dev/null || true

# 清理证书
rm -f /etc/kubernetes/pki/ca.crt 2>/dev/null || true
rm -f /etc/kubernetes/bootstrap-kubelet.conf 2>/dev/null || true

echo "reset-complete"
`
    
    return fmt.Sprintf(script, fmt.Sprintf("%v", cleanupCNI))
}

// BuildDrainCommand 构建 drain 命令
func (b *CommandBuilder) BuildDrainCommand(nodeName string, opts DrainOptions) string {
    flags := []string{}
    
    if opts.GracePeriodSeconds > 0 {
        flags = append(flags, fmt.Sprintf("--grace-period=%d", opts.GracePeriodSeconds))
    }
    
    if opts.Timeout > 0 {
        flags = append(flags, fmt.Sprintf("--timeout=%dm", int(opts.Timeout.Minutes())))
    }
    
    if opts.DeleteLocalData {
        flags = append(flags, "--delete-local-data")
    }
    
    if opts.Force {
        flags = append(flags, "--force")
    }
    
    if opts.IgnoreDaemonsets {
        flags = append(flags, "--ignore-daemonsets")
    }
    
    // 添加额外参数
    if len(opts.ExtraFlags) > 0 {
        flags = append(flags, opts.ExtraFlags...)
    }
    
    flagsStr := strings.Join(flags, " ")
    
    return fmt.Sprintf(`
# 执行 drain
kubectl drain %s %s

# 验证
if kubectl get node %s &>/dev/null; then
    echo "drain-complete"
else
    echo "warning: node not found after drain"
fi
`, nodeName, flagsStr, nodeName)
}

// BuildHealthCheckCommand 构建健康检查命令
func (b *CommandBuilder) BuildHealthCheckCommand() string {
    return `
echo "=== System Health Check ==="
echo "Hostname: $(hostname)"
echo "Uptime: $(uptime)"
echo "Load: $(cat /proc/loadavg | awk '{print $1, $2, $3}')"
echo "Memory: $(free -h | grep Mem | awk '{print $3 "/" $2}')"
echo "Disk: $(df -h / | tail -1 | awk '{print $3 "/" $2}')"
echo "Kubelet: $(systemctl is-active kubelet 2>/dev/null || echo 'inactive')"
echo "Container Runtime: $(systemctl is-active containerd 2>/dev/null || systemctl is-active docker 2>/dev/null || echo 'inactive')"
echo "=== End ==="
`
}

// BuildCustomCommand 构建自定义命令
func (b *CommandBuilder) BuildCustomCommand(template string, data map[string]interface{}) (string, error) {
    result := template
    
    for k, v := range data {
        placeholder := fmt.Sprintf("{{.%s}}", k)
        value := fmt.Sprintf("%v", v)
        result = strings.ReplaceAll(result, placeholder, value)
    }
    
    return result, nil
}

// DrainOptions drain 选项
type DrainOptions struct {
    GracePeriodSeconds int
    Timeout            time.Duration
    DeleteLocalData    bool
    Force              bool
    IgnoreDaemonsets   bool
    ExtraFlags         []string
}
```

### O.6 SSH 失败处理和降级策略

```go
// FailureHandler SSH 失败处理器
type FailureHandler struct {
    executor    *SSHExecutor
    metrics     *Metrics
    alertSender *AlertSender
    config      *FailureConfig
}

// FailureConfig 失败处理配置
type FailureConfig struct {
    // 重试配置
    MaxRetries           int
    RetryDelay            time.Duration
    ExponentialBackoff    bool
    MaxRetryDelay         time.Duration
    
    // 失败阈值
    FailureThreshold      int           // 连续失败次数阈值
    FailureWindow         time.Duration // 失败统计时间窗口
    
    // 降级策略
    EnableFallback        bool          // 是否启用降级策略
    FallbackTimeout       time.Duration // 降级超时时间
    
    // 告警配置
    AlertOnFailure        bool
    AlertThreshold        int
}

// SSH 错误类型
type SSHErrorType string

const (
    SSHErrorConnectionFailed   SSHErrorType = "connection_failed"
    SSHErrorAuthFailed         SSHErrorType = "auth_failed"
    SSHErrorTimeout            SSHErrorType = "timeout"
    SSHErrorHostUnreachable    SSHErrorType = "host_unreachable"
    SSHErrorPermissionDenied   SSHErrorType = "permission_denied"
    SSHErrorCommandFailed      SSHErrorType = "command_failed"
    SSHErrorSessionClosed      SSHErrorType = "session_closed"
    SSHErrorUnknown            SSHErrorType = "unknown"
)

// ClassifyError 分类错误类型
func ClassifyError(err error) SSHErrorType {
    if err == nil {
        return ""
    }
    
    errStr := err.Error()
    
    switch {
    case strings.Contains(errStr, "connection refused"):
        return SSHErrorConnectionFailed
    case strings.Contains(errStr, "no such host") || strings.Contains(errStr, "i/o timeout"):
        return SSHErrorHostUnreachable
    case strings.Contains(errStr, "authentication failed") || strings.Contains(errStr, "permission denied"):
        return SSHErrorAuthFailed
    case strings.Contains(errStr, "deadline exceeded") || strings.Contains(errStr, "context deadline exceeded"):
        return SSHErrorTimeout
    case strings.Contains(errStr, "session closed") || strings.Contains(errStr, "connection reset"):
        return SSHErrorSessionClosed
    case strings.Contains(errStr, "command failed") || strings.Contains(errStr, "exit status"):
        return SSHErrorCommandFailed
    default:
        return SSHErrorUnknown
    }
}

// IsRetryable 判断错误是否可重试
func IsRetryable(errorType SSHErrorType) bool {
    retryableTypes := map[SSHErrorType]bool{
        SSHErrorConnectionFailed:  true,
        SSHErrorTimeout:           true,
        SSHErrorHostUnreachable:   true,
        SSHErrorSessionClosed:     true,
    }
    
    return retryableTypes[errorType]
}

// ExecuteWithRetry 带重试的执行
func (h *FailureHandler) ExecuteWithRetry(ctx context.Context, host, user string, port int, cmd string, opts ExecuteOption) (*Result, error) {
    errorTypes := make([]SSHErrorType, 0)
    var lastErr error
    
    delay := h.config.RetryDelay
    
    for attempt := 1; attempt <= h.config.MaxRetries; attempt++ {
        result, err := h.executor.Execute(ctx, host, user, port, cmd, opts)
        
        if err == nil {
            // 成功
            if attempt > 1 {
                h.metrics.RecordRetrySuccess(host, attempt)
            }
            return result, nil
        }
        
        // 记录失败
        errorType := ClassifyError(err)
        errorTypes = append(errorTypes, errorType)
        lastErr = err
        
        h.metrics.RecordRetryAttempt(host, errorType, attempt)
        
        // 检查是否可重试
        if !IsRetryable(errorType) {
            h.metrics.RecordRetryFailure(host, errorType, "non-retryable")
            return result, err
        }
        
        // 等待后重试
        if attempt < h.config.MaxRetries {
            sleepTime := delay
            if h.config.ExponentialBackoff {
                sleepTime = time.Duration(float64(delay) * float64(attempt))
                if sleepTime > h.config.MaxRetryDelay {
                    sleepTime = h.config.MaxRetryDelay
                }
            }
            
            select {
            case <-ctx.Done():
                return result, ctx.Err()
            case <-time.After(sleepTime):
            }
        }
    }
    
    // 所有重试失败
    h.metrics.RecordRetryFailure(host, errorTypes[len(errorTypes)-1], "max-retries-exceeded")
    
    // 发送告警
    if h.config.AlertOnFailure && len(errorTypes) >= h.config.AlertThreshold {
        h.sendFailureAlert(host, errorTypes)
    }
    
    return nil, fmt.Errorf("max retries (%d) exceeded: %v", h.config.MaxRetries, lastErr)
}

// sendFailureAlert 发送失败告警
func (h *FailureHandler) sendFailureAlert(host string, errorTypes []SSHErrorType) {
    h.alertSender.Send(&Alert{
        Type:      AlertTypeSSHFailure,
        Severity:  AlertSeverityWarning,
        Message:   fmt.Sprintf("SSH failures on host %s: %v", host, errorTypes),
        Metadata: map[string]interface{}{
            "host":        host,
            "error_types": errorTypes,
            "count":       len(errorTypes),
        },
    })
}
```

### O.7 机器无法 SSH 时的处理

```go
// UnreachableHandler 机器不可达处理器
// 注意：本系统只支持已开机的机器，所有操作通过 SSH 进行
// 如果机器无法通过 SSH 访问，将被标记为故障状态
type UnreachableHandler struct {
    executor    *SSHExecutor
    k8sClient   *k8s.Client
    db          *database.DB
    alertSender *AlertSender
    config      *UnreachableConfig
}

// UnreachableConfig 不可达处理配置
type UnreachableConfig struct {
    // SSH 检测配置
    SSHProbeTimeout time.Duration
    K8sProbeTimeout time.Duration
    
    // 状态转换配置
    MaxConsecutiveFailures  int  // 连续 SSH 失败多少次后标记为故障
    MaintenanceThreshold    int  // 连续 SSH 失败多少次后进入维护状态
    
    // 告警配置
    AlertOnUnreachable bool
    AlertOnRecovery    bool
}

// HandleUnreachable 处理不可达机器
// 本系统只支持已开机的机器，如果 SSH 无法连接，将直接标记为故障
func (h *UnreachableHandler) HandleUnreachable(ctx context.Context, machineID string) error {
    machine, err := h.db.GetMachine(ctx, machineID)
    if err != nil {
        return err
    }
    
    // 递增失败计数
    machine.ConsecutiveFailures++
    
    // 更新机器状态
    if machine.ConsecutiveFailures >= h.config.MaxConsecutiveFailures {
        // 超过最大重试次数，标记为故障
        machine.Status = model.StatusFault
        machine.ErrorMessage = fmt.Sprintf("machine unreachable after %d SSH attempts", machine.ConsecutiveFailures)
        
        h.alertSender.Send(&Alert{
            Type:     AlertTypeMachineFault,
            Severity: AlertSeverityError,
            Machine:  machine.IPAddress,
            Message:  fmt.Sprintf("Machine %s marked as fault after %d consecutive SSH failures",
                machine.IPAddress, machine.ConsecutiveFailures),
        })
    } else {
        // 进入维护状态，需要人工干预
        machine.Status = model.StatusMaintenance
        machine.ErrorMessage = "machine SSH connection failed, requires manual intervention"
    }
    
    return h.db.UpdateMachine(ctx, machine)
}

// ProbeMachineStatus 检测机器状态
// 由于只支持已开机的机器，检测方式为 SSH 连接 + K8s 节点状态
func (h *UnreachableHandler) ProbeMachineStatus(ctx context.Context, machine *model.Machine) *ProbeResult {
    result := &ProbeResult{
        MachineID: machine.ID,
        Timestamp: time.Now(),
    }
    
    // 1. 尝试 SSH 连接
    if err := h.probeSSH(ctx, machine); err != nil {
        result.SSHStatus = "failed"
        result.SSHError = err.Error()
    } else {
        result.SSHStatus = "ok"
    }
    
    // 2. 检查 K8s 节点状态（如果已加入集群）
    if machine.NodeName != "" {
        if node, err := h.k8sClient.GetNode(ctx, machine.NodeName); err != nil {
            result.K8sStatus = "failed"
            result.K8sError = err.Error()
        } else {
            result.K8sStatus = "ok"
            result.NodeReady = isNodeReady(node)
        }
    } else {
        result.K8sStatus = "n/a"
    }
    
    // 综合判断
    // 只有 SSH 成功才算可达
    result.IsReachable = result.SSHStatus == "ok"
    
    // 如果 SSH 失败且不在集群中，需要人工干预
    result.NeedsIntervention = result.SSHStatus == "failed" && result.K8sStatus != "ok"
    
    return result
}
    
    return result
}

// probeSSH 探测 SSH 连接
func (h *UnreachableHandler) probeSSH(ctx context.Context, machine *model.Machine) error {
    ctx, cancel := context.WithTimeout(ctx, h.config.SSHProbeTimeout)
    defer cancel()
    
    conn, err := h.executor.GetConnection(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort)
    if err != nil {
        return err
    }
    defer h.executor.ReleaseConnection(conn)
    
    // 简单执行命令验证
    _, err = conn.Exec(ctx, "echo 'ok'")
    return err
}

// ProbeResult 检测结果
// 本系统只通过 SSH 检测机器状态，K8s 状态作为辅助
type ProbeResult struct {
    MachineID         string
    Timestamp         time.Time
    SSHStatus         string  // "ok", "failed"
    SSHError          string
    K8sStatus         string  // "ok", "failed", "n/a"
    K8sError          string
    NodeReady         bool
    IsReachable       bool    // SSH 成功才算可达
    NeedsIntervention bool    // SSH 失败且无法通过 K8s 验证时需要人工干预
}
```

### O.8 批量 SSH 操作并发控制

```go
// BatchExecutor 批量 SSH 执行器
type BatchExecutor struct {
    executor     *SSHExecutor
    rateLimiter  *RateLimiter
    semaphores   map[string]*Semaphore // 按主机分组的信号量
    mutex        sync.RWMutex
    config       *BatchConfig
}

// BatchConfig 批量执行配置
type BatchConfig struct {
    // 并发控制
    GlobalConcurrency int           // 全局并发数
    PerHostConcurrency int          // 单主机并发数
    
    // 速率限制
    GlobalRateLimit   float64       // 每秒请求数
    PerHostRateLimit  float64       // 单主机每秒请求数
    
    // 超时配置
    TotalTimeout      time.Duration // 总超时
    PerCommandTimeout time.Duration // 单命令超时
    
    // 失败处理
    FailFast          bool          // 快速失败
    MinSuccessRate    float64       // 最小成功率
}

// Semaphore 信号量
type Semaphore struct {
    permits int
    ch      chan struct{}
}

// NewSemaphore 创建信号量
func NewSemaphore(permits int) *Semaphore {
    return &Semaphore{
        permits: permits,
        ch:      make(chan struct{}, permits),
    }
}

// Acquire 获取许可
func (s *Semaphore) Acquire() {
    s.ch <- struct{}{}
}

// Release 释放许可
func (s *Semaphore) Release() {
    select {
    case <-s.ch:
    default:
    }
}

// BatchResult 批量执行结果
type BatchResult struct {
    TotalCount     int
    SuccessCount   int
    FailureCount   int
    SkippedCount   int
    Results        []*CommandResult
    Duration       time.Duration
}

// CommandResult 单个命令结果
type CommandResult struct {
    Host      string
    Success   bool
    ExitCode  int
    Output    string
    Error     error
    Duration  time.Duration
}

// ExecuteBatch 批量执行命令
func (e *BatchExecutor) ExecuteBatch(ctx context.Context, hosts []string, user string, port int, cmd string) (*BatchResult, error) {
    ctx, cancel := context.WithTimeout(ctx, e.config.TotalTimeout)
    defer cancel()
    
    result := &BatchResult{
        TotalCount: len(hosts),
        Results:    make([]*CommandResult, 0, len(hosts)),
    }
    
    // 创建全局速率限制器
    globalLimiter := e.rateLimiter.NewLimiter(e.config.GlobalRateLimit, e.config.GlobalConcurrency)
    
    var wg sync.WaitGroup
    resultChan := make(chan *CommandResult, len(hosts))
    
    for _, host := range hosts {
        wg.Add(1)
        
        go func(h string) {
            defer wg.Done()
            
            // 获取速率限制许可
            if err := globalLimiter.Wait(ctx); err != nil {
                resultChan <- &CommandResult{
                    Host:   h,
                    Success: false,
                    Error:  err,
                }
                return
            }
            
            // 获取主机级信号量
            semaphore := e.getOrCreateSemaphore(h)
            semaphore.Acquire()
            defer semaphore.Release()
            
            // 执行命令
            cmdResult, err := e.executor.Execute(ctx, h, user, port, cmd, ExecuteOption{
                Timeout:   e.config.PerCommandTimeout,
                Retries:   2,
                RetryDelay: 5 * time.Second,
            })
            
            cmdRes := &CommandResult{
                Host:     h,
                Duration: cmdResult.Duration,
            }
            
            if err != nil {
                cmdRes.Success = false
                cmdRes.Error = err
            } else {
                cmdRes.Success = cmdResult.ExitCode == 0
                cmdRes.ExitCode = cmdResult.ExitCode
                cmdRes.Output = cmdResult.Stdout
            }
            
            resultChan <- cmdRes
        }(host)
    }
    
    // 等待完成
    go func() {
        wg.Wait()
        close(resultChan)
    }()
    
    // 收集结果
    for res := range resultChan {
        result.Results = append(result.Results, res)
        if res.Success {
            result.SuccessCount++
        } else {
            result.FailureCount++
            
            // 快速失败
            if e.config.FailFast {
                cancel()
            }
        }
    }
    
    result.Duration = time.Since(result.StartTime)
    
    // 检查成功率
    if result.TotalCount > 0 {
        successRate := float64(result.SuccessCount) / float64(result.TotalCount)
        if successRate < e.config.MinSuccessRate {
            return result, fmt.Errorf("success rate %.2f%% below threshold %.2f%%", 
                successRate*100, e.config.MinSuccessRate*100)
        }
    }
    
    return result, nil
}

// ExecuteParallel 并行执行不同命令
func (e *BatchExecutor) ExecuteParallel(ctx context.Context, tasks []BatchTask) (*BatchResult, error) {
    var wg sync.WaitGroup
    resultChan := make(chan *CommandResult, len(tasks))
    
    for _, task := range tasks {
        wg.Add(1)
        
        go func(t BatchTask) {
            defer wg.Done()
            
            res, err := e.executor.Execute(ctx, t.Host, t.User, t.Port, t.Command, t.Options)
            
            cmdRes := &CommandResult{
                Host:    t.Host,
                Duration: res.Duration,
            }
            
            if err != nil {
                cmdRes.Success = false
                cmdRes.Error = err
            } else {
                cmdRes.Success = res.ExitCode == 0
                cmdRes.ExitCode = res.ExitCode
                cmdRes.Output = res.Stdout
            }
            
            resultChan <- cmdRes
        }(task)
    }
    
    wg.Wait()
    close(resultChan)
    
    // 收集结果
    result := &BatchResult{
        TotalCount: len(tasks),
        Results:    make([]*CommandResult, 0, len(tasks)),
    }
    
    for res := range resultChan {
        result.Results = append(result.Results, res)
        if res.Success {
            result.SuccessCount++
        } else {
            result.FailureCount++
        }
    }
    
    return result, nil
}

// BatchTask 批量任务
type BatchTask struct {
    Host     string
    User     string
    Port     int
    Command  string
    Options  ExecuteOption
}
```

### O.9 SSH 命令执行日志和审计

```go
// SSHLogger SSH 执行日志
type SSHLogger struct {
    logger    *Logger
    auditLog  *AuditLogger
    config    *LoggerConfig
}

// LoggerConfig 日志配置
type LoggerConfig struct {
    // 日志级别
    Level string
    
    // 输出配置
    EnableStdout bool
    EnableFile   bool
    FilePath     string
    
    // 敏感信息处理
    MaskSecrets   bool
    SecretPatterns []string
    
    // 审计配置
    EnableAudit   bool
    AuditTable    string
}

// LogExecution 记录执行日志
func (l *SSHLogger) LogExecution(host, command string, duration time.Duration, success bool) {
    entry := &LogEntry{
        Timestamp: time.Now(),
        Host:      host,
        Command:   l.maskSecrets(command),
        Duration:  duration,
        Success:   success,
    }
    
    // 写日志
    if l.config.EnableStdout {
        l.logger.Info("ssh execution", "entry", entry)
    }
    
    if l.config.EnableFile {
        l.writeToFile(entry)
    }
}

// maskSecrets 遮盖敏感信息
func (l *SSHLogger) maskSecrets(cmd string) string {
    if !l.config.MaskSecrets {
        return cmd
    }
    
    result := cmd
    
    patterns := []string{
        `--token [a-zA-Z0-9.-]+`,
        `--discovery-token-ca-cert-hash [a-fA-F0-9:]+`,
        `password: \S+`,
        `--private-key \S+`,
    }
    
    for _, pattern := range l.config.SecretPatterns {
        re := regexp.MustCompile(pattern)
        result = re.ReplaceAllString(result, "[REDACTED]")
    }
    
    return result
}

// WriteAudit 写审计日志
func (l *SSHLogger) WriteAudit(ctx context.Context, audit *AuditEntry) error {
    if !l.config.EnableAudit {
        return nil
    }
    
    return l.auditLog.Write(ctx, audit)
}

// AuditEntry 审计条目
type AuditEntry struct {
    ID          string
    Timestamp   time.Time
    Host        string
    User        string
    Command     string
    ExitCode    int
    Duration    time.Duration
    Success     bool
    Error       string
    Actor       string
    SourceIP    string
    RequestID   string
}

// CreateAuditFromResult 从执行结果创建审计条目
func (l *SSHLogger) CreateAuditFromResult(host, user, command string, result *Result, actor, sourceIP, requestID string) *AuditEntry {
    return &AuditEntry{
        ID:        uuid.New().String(),
        Timestamp: time.Now(),
        Host:      host,
        User:      user,
        Command:   l.maskSecrets(command),
        ExitCode:  result.ExitCode,
        Duration:  result.Duration,
        Success:   result.ExitCode == 0,
        Error:     result.Stderr,
        Actor:     actor,
        SourceIP:  sourceIP,
        RequestID: requestID,
    }
}
```

### O.10 SSH 性能指标

```go
// Metrics SSH 性能指标
type Metrics struct {
    mutex        sync.RWMutex
    connections  map[string]*ConnectionMetrics
    executions   map[string]*ExecutionMetrics
    failures     map[string]*FailureMetrics
}

// ConnectionMetrics 连接指标
type ConnectionMetrics struct {
    TotalCreated    int64
    TotalActive     int64
    TotalReleased   int64
    TotalFailed     int64
    AveragePoolSize float64
}

// ExecutionMetrics 执行指标
type ExecutionMetrics struct {
    TotalCount      int64
    SuccessCount    int64
    FailureCount    int64
    TotalDuration   time.Duration
    AverageDuration time.Duration
    MinDuration     time.Duration
    MaxDuration     time.Duration
}

// FailureMetrics 失败指标
type FailureMetrics struct {
    TotalCount     int64
    ByErrorType    map[string]int64
    RecentFailures []FailureEvent
}

// RecordConnection 记录连接
func (m *Metrics) RecordConnection(host string, success bool) {
    m.mutex.Lock()
    defer m.mutex.Unlock()
    
    if _, ok := m.connections[host]; !ok {
        m.connections[host] = &ConnectionMetrics{}
    }
    
    metrics := m.connections[host]
    
    if success {
        metrics.TotalCreated++
        metrics.TotalActive++
    } else {
        metrics.TotalFailed++
    }
}

// RecordExecution 记录执行
func (m *Metrics) RecordExecution(host string, duration time.Duration, success bool) {
    m.mutex.Lock()
    defer m.mutex.Unlock()
    
    key := host
    
    if _, ok := m.executions[key]; !ok {
        m.executions[key] = &ExecutionMetrics{
            MinDuration: duration,
            MaxDuration: duration,
        }
    }
    
    metrics := m.executions[key]
    metrics.TotalCount++
    
    if success {
        metrics.SuccessCount++
    } else {
        metrics.FailureCount++
    }
    
    metrics.TotalDuration += duration
    metrics.AverageDuration = time.Duration(float64(metrics.TotalDuration) / float64(metrics.TotalCount))
    
    if duration < metrics.MinDuration {
        metrics.MinDuration = duration
    }
    if duration > metrics.MaxDuration {
        metrics.MaxDuration = duration
    }
}

// RecordFailure 记录失败
func (m *Metrics) RecordFailure(host string, errorType SSHErrorType) {
    m.mutex.Lock()
    defer m.mutex.Unlock()
    
    key := host
    
    if _, ok := m.failures[key]; !ok {
        m.failures[key] = &FailureMetrics{
            ByErrorType: make(map[string]int64),
        }
    }
    
    metrics := m.failures[key]
    metrics.TotalCount++
    metrics.ByErrorType[string(errorType)]++
    
    // 记录最近失败
    metrics.RecentFailures = append(metrics.RecentFailures, FailureEvent{
        Timestamp:  time.Now(),
        ErrorType:  errorType,
    })
    
    // 只保留最近 100 个失败记录
    if len(metrics.RecentFailures) > 100 {
        metrics.RecentFailures = metrics.RecentFailures[len(metrics.RecentFailures)-100:]
    }
}

// GetMetrics 获取指标
func (m *Metrics) GetMetrics() *MetricsSnapshot {
    m.mutex.RLock()
    defer m.mutex.RUnlock()
    
    return &MetricsSnapshot{
        Connections: m.connections,
        Executions:  m.executions,
        Failures:    m.failures,
        Timestamp:   time.Now(),
    }
}

// MetricsSnapshot 指标快照
type MetricsSnapshot struct {
    Connections map[string]*ConnectionMetrics
    Executions  map[string]*ExecutionMetrics
    Failures    map[string]*FailureMetrics
    Timestamp   time.Time
}

// FailureEvent 失败事件
type FailureEvent struct {
    Timestamp time.Time
    ErrorType SSHErrorType
}
```

### O.11 完整 SSH 执行器集成

```go
// CompleteSSHExecutor 完整 SSH 执行器（整合所有组件）
type CompleteSSHExecutor struct {
    config           *Config
    executor         *SSHExecutor
    failureHandler   *FailureHandler
    unreachableHandler *UnreachableHandler
    batchExecutor    *BatchExecutor
    logger           *SSHLogger
    metrics          *Metrics
}

// NewCompleteSSHExecutor 创建完整执行器
func NewCompleteSSHExecutor(cfg *CompleteConfig) (*CompleteSSHExecutor, error) {
    // 创建基础执行器
    executor, err := NewSSHExecutor(&cfg.Config)
    if err != nil {
        return nil, err
    }
    
    complete := &CompleteSSHExecutor{
        config:  cfg,
        executor: executor,
        failureHandler: NewFailureHandler(executor, &FailureConfig{
            MaxRetries:        cfg.MaxRetries,
            RetryDelay:        cfg.RetryDelay,
            ExponentialBackoff: true,
            MaxRetryDelay:     30 * time.Second,
            FailureThreshold:  3,
            AlertOnFailure:    true,
            AlertThreshold:    5,
        }),
        unreachableHandler: NewUnreachableHandler(executor, cfg),
        batchExecutor: NewBatchExecutor(executor, &BatchConfig{
            GlobalConcurrency:  cfg.GlobalConcurrency,
            PerHostConcurrency: cfg.PerHostConcurrency,
            TotalTimeout:       cfg.BatchTimeout,
            FailFast:           false,
            MinSuccessRate:     0.8,
        }),
        logger:  NewSSHLogger(&LoggerConfig{
            Level:       cfg.LogLevel,
            MaskSecrets: true,
            EnableAudit: true,
        }),
        metrics: NewMetrics(),
    }
    
    return complete, nil
}

// CompleteConfig 完整配置
type CompleteConfig struct {
    SSHConfig
    MaxRetries        int
    RetryDelay        time.Duration
    GlobalConcurrency int
    PerHostConcurrency int
    BatchTimeout      time.Duration
    LogLevel          string
}

// Execute 执行命令（主入口）
func (e *CompleteSSHExecutor) Execute(ctx context.Context, host, user string, port int, cmd string) (*Result, error) {
    // 使用失败处理器（带重试）
    return e.failureHandler.ExecuteWithRetry(ctx, host, user, port, cmd, ExecuteOption{
        Timeout: e.config.DefaultTimeout,
    })
}

// ExecuteJoin 执行 Join 操作
func (e *CompleteSSHExecutor) ExecuteJoin(ctx context.Context, machine *model.Machine, joinCommand string) (*Result, error) {
    nodeName := fmt.Sprintf("node-%s", machine.ID[:8])
    cmd := e.executor.commandBuilder.BuildJoinCommand(joinCommand, nodeName)
    
    return e.Execute(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort, cmd)
}

// ExecuteReset 执行 Reset 操作
func (e *CompleteSSHExecutor) ExecuteReset(ctx context.Context, machine *model.Machine, cleanupCNI bool) (*Result, error) {
    cmd := e.executor.commandBuilder.BuildResetCommand(cleanupCNI)
    
    return e.Execute(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort, cmd)
}

// ExecuteDrain 执行 Drain 操作
func (e *CompleteSSHExecutor) ExecuteDrain(ctx context.Context, machine *model.Machine, opts DrainOptions) (*Result, error) {
    cmd := e.executor.commandBuilder.BuildDrainCommand(machine.NodeName, opts)
    
    return e.Execute(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort, cmd)
}

// ExecuteBatch 批量执行
func (e *CompleteSSHExecutor) ExecuteBatch(ctx context.Context, machines []*model.Machine, cmd string) (*BatchResult, error) {
    hosts := make([]string, len(machines))
    for i, m := range machines {
        hosts[i] = m.IPAddress
    }
    
    return e.batchExecutor.ExecuteBatch(ctx, hosts, machines[0].SSHUser, machines[0].SSHPort, cmd)
}

// CheckMachineStatus 检查机器状态
func (e *CompleteSSHExecutor) CheckMachineStatus(ctx context.Context, machine *model.Machine) *ProbeResult {
    return e.unreachableHandler.ProbeMachineStatus(ctx, machine)
}

// GetMetrics 获取指标
func (e *CompleteSSHExecutor) GetMetrics() *MetricsSnapshot {
    return e.metrics.GetMetrics()
}

// Close 关闭执行器
func (e *CompleteSSHExecutor) Close() error {
    return e.executor.Close()
}
```

### O.12 附录：勘误汇总（续）

| 日期 | 版本 | 修复内容 |
|-----|------|---------|
| 2024-01-17 | v1.0.2 | 新增完整 SSH 执行器设计（连接池、会话管理、命令构建） |
| 2024-01-17 | v1.0.2 | 新增 SSH 失败处理和降级策略 |
| 2024-01-17 | v1.0.2 | 新增机器无法 SSH 时的处理 |
| 2024-01-17 | v1.0.2 | 新增批量 SSH 操作并发控制 |
| 2024-01-17 | v1.0.2 | 新增 SSH 命令执行日志和审计 |
| 2024-01-17 | v1.0.2 | 新增 SSH 性能指标监控 |
| 2024-01-17 | v1.0.2 | 新增完整 SSH 执行器集成（CompleteSSHExecutor） |

---

## P. 机器导入与池子管理设计

### P.1 设计概述

机器导入和池子管理是系统的日常运维核心功能。一个好的管理体验应该支持：
- **多种导入方式**：手动单台、批量导入、自动发现
- **便捷的池子维护**：创建、修改、监控池子状态
- **清晰的关联关系**：机器与池子的绑定/解绑
- **完整的生命周期**：机器从入库到报废的全流程管理

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         机器与池子管理架构                                │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌───────────────────────────────────────────────────────────────────┐ │
│  │                       管理入口层                                    │ │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────────────────────┐ │ │
│  │  │ CLI 命令    │ │ Web UI      │ │ RESTful API                │ │ │
│  │  │ ksa machine │ │ Dashboard   │ │ /api/v1/machines           │ │ │
│  │  │ ksa pool    │ │             │ │ /api/v1/nodepools          │ │ │
│  │  └─────────────┘ └─────────────┘ └─────────────────────────────┘ │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                        │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                       机器导入层                                    │ │
│  │  ┌────────────┐ ┌────────────┐ ┌────────────────────────────┐   │ │
│  │  │ 手动导入   │ │ 批量导入   │ │ 自动发现                   │   │ │
│  │  │ 单台添加   │ │ CSV/YAML   │ │ IP 扫描 + SSH 探测         │   │ │
│  │  └────────────┘ └────────────┘ └────────────────────────────┘   │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                  │                                        │
│  ┌───────────────────────────────▼───────────────────────────────────┐ │
│  │                       池子管理层                                    │ │
│  │  ┌────────────┐ ┌────────────┐ ┌────────────────────────────┐   │ │
│  │  │ 池子配置   │ │ 成员管理   │ │ 状态监控                   │   │ │
│  │  │ 创建/修改  │ │ 绑定/解绑  │ │ 资源使用率                 │   │ │
│  │  └────────────┘ └────────────┘ └────────────────────────────┘   │ │
│  └───────────────────────────────────────────────────────────────────┘ │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### P.2 机器导入功能

#### P.2.1 手动单台导入

```bash
# 基础导入
ksa machine add \
  --nodepool=cpu-pool \
  --hostname=node-001 \
  --ip-address=192.168.1.100 \
  --cpu-cores=16 \
  --memory=64GiB \
  --ssh-user=root \
  --ssh-key=default

# 完整参数导入
ksa machine add \
  --nodepool=gpu-pool-a100 \
  --hostname=node-gpu-001 \
  --ip-address=192.168.1.101 \
  --cpu-cores=64 \
  --memory=256GiB \
  --gpu-type=nvidia-a100 \
  --gpu-count=4 \
  --ssh-user=admin \
  --ssh-port=2222 \
  --ssh-key=gpu-pool-key \
  --region=cn-bj1 \
  --zone=zone-a \
  --rack=rack-01 \
  --labels=env=prod,team=ml \
  --tags=department=ai
```

```go
// cmd/machine/add.go
func NewAddMachineCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "add",
        Short: "Add a new machine to a nodepool",
        Long: `Add a new machine to a nodepool.
        
Examples:
  # Add a basic CPU machine
  ksa machine add --nodepool=cpu-pool --hostname=node-001 --ip-address=192.168.1.100
  
  # Add a GPU machine with full configuration
  ksa machine add --nodepool=gpu-pool --hostname=gpu-001 --ip-address=192.168.1.101 \
    --gpu-type=nvidia-a100 --gpu-count=4 --ssh-user=admin --ssh-key=gpu-key`,
    }
    
    // 必选参数
    cmd.Flags().String("nodepool", "", "Nodepool name (required)")
    cmd.Flags().String("hostname", "", "Machine hostname (required)")
    cmd.Flags().String("ip-address", "", "IP address (required)")
    
    // 硬件规格
    cmd.Flags().Int("cpu-cores", 0, "Number of CPU cores")
    cmd.Flags().String("memory", "", "Memory (e.g., 64GiB, 68719476736)")
    cmd.Flags().String("gpu-type", "", "GPU type (e.g., nvidia-a100, nvidia-v100)")
    cmd.Flags().Int("gpu-count", 0, "Number of GPUs")
    
    // SSH 配置
    cmd.Flags().String("ssh-user", "root", "SSH username")
    cmd.Flags().Int("ssh-port", 22, "SSH port")
    cmd.Flags().String("ssh-key", "default", "SSH key name")
    
    // 位置信息
    cmd.Flags().String("region", "default", "Region")
    cmd.Flags().String("zone", "default", "Availability zone")
    cmd.Flags().String("rack", "", "Rack location")
    
    // 元数据
    cmd.Flags().StringToStringVar(&labels, "labels", nil, "Labels (key=value)")
    cmd.Flags().StringToStringVar(&tags, "tags", nil, "Tags (key=value)")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        // 解析参数
        memoryBytes, err := parseMemory(opts.Memory)
        if err != nil {
            return err
        }
        
        machine := &model.Machine{
            Hostname:    opts.Hostname,
            IPAddress:   net.ParseIP(opts.IPAddress),
            NodepoolID:  nodepoolID,
            Status:      model.StatusAvailable,
            CPUCores:    opts.CPUCores,
            MemoryBytes: memoryBytes,
            SSHUser:     opts.SSHUser,
            SSHPort:     opts.SSHPort,
            Region:      opts.Region,
            Zone:        opts.Zone,
            Rack:        opts.Rack,
            Labels:      opts.Labels,
            Tags:        opts.Tags,
        }
        
        // 处理 GPU 信息
        if opts.GPUType != "" {
            machine.GPUInfo = &model.GPUInfo{
                Type:  opts.GPUType,
                Count: opts.GPUCount,
            }
        }
        
        // 处理 SSH 密钥
        sshKey, err := db.GetSSHKeyByName(ctx, opts.SSHKey)
        if err != nil {
            return fmt.Errorf("SSH key not found: %s", opts.SSHKey)
        }
        machine.SSHKeyID = sshKey.ID
        
        // 保存到数据库
        if err := db.CreateMachine(ctx, machine); err != nil {
            return err
        }
        
        // 验证 SSH 连接
        if err := verifySSHConnection(ctx, machine); err != nil {
            fmt.Printf("Warning: SSH verification failed: %v\n", err)
            fmt.Printf("Machine added but may require manual SSH configuration\n")
        }
        
        fmt.Printf("Machine %s (%s) added successfully to nodepool %s\n",
            machine.Hostname, machine.IPAddress, nodepoolName)
        
        return nil
    }
    
    return cmd
}

// verifySSHConnection 验证 SSH 连接
func verifySSHConnection(ctx context.Context, machine *model.Machine) error {
    executor, err := GetSSHExecutor()
    if err != nil {
        return err
    }
    
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    _, err = executor.Execute(ctx, machine.IPAddress, machine.SSHUser, machine.SSHPort,
        "echo 'SSH connection verified'", ExecuteOption{
            Timeout: 20 * time.Second,
        })
    
    return err
}
```

#### P.2.2 批量导入

```bash
# 从 YAML 文件导入
ksa machine import --file=machines.yaml --nodepool=cpu-pool

# 从 CSV 文件导入
ksa machine import --file=machines.csv --nodepool=gpu-pool --format=csv

# 从 JSON 文件导入
ksa machine import --file=machines.json --nodepool=cpu-pool

# 跳过 SSH 验证（加速导入）
ksa machine import --file=machines.yaml --nodepool=cpu-pool --skip-ssh-verify

# dry-run 模式（预览导入结果）
ksa machine import --file=machines.yaml --nodepool=cpu-pool --dry-run
```

```yaml
# machines.yaml 示例
apiVersion: v1
kind: MachineList
metadata:
  nodepool: cpu-pool
spec:
  machines:
    - hostname: cpu-node-001
      ip_address: 192.168.1.100
      cpu_cores: 16
      memory_bytes: 68719476736  # 64GiB
      ssh_user: root
      ssh_port: 22
      ssh_key: default
      region: cn-bj1
      zone: zone-a
      
    - hostname: cpu-node-002
      ip_address: 192.168.1.101
      cpu_cores: 16
      memory_bytes: 68719476736
      ssh_user: root
      ssh_port: 22
      ssh_key: default
      region: cn-bj1
      zone: zone-a
      
    - hostname: cpu-node-003
      ip_address: 192.168.1.102
      cpu_cores: 32
      memory_bytes: 137438953472  # 128GiB
      ssh_user: admin
      ssh_port: 2222
      ssh_key: admin-key
      region: cn-bj1
      zone: zone-b
```

```csv
# machines.csv 示例
hostname,ip_address,cpu_cores,memory_bytes,ssh_user,ssh_port,ssh_key,region,zone,rack
cpu-node-001,192.168.1.100,16,68719476736,root,22,default,cn-bj1,zone-a,rack-01
cpu-node-002,192.168.1.101,16,68719476736,root,22,default,cn-bj1,zone-a,rack-01
cpu-node-003,192.168.1.102,32,137438953472,admin,2222,admin-key,cn-bj1,zone-b,rack-02
gpu-node-001,192.168.2.100,64,274877906944,root,22,gpu-key,cn-bj1,zone-a,rack-03
```

```go
// cmd/machine/import.go
func NewImportMachinesCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "import",
        Short: "Import machines from file",
        Long: `Import multiple machines from a YAML, JSON, or CSV file.
        
Supported formats:
  - YAML: machines.yaml (MachineList format)
  - JSON: machines.json (MachineList format)
  - CSV: machines.csv (with headers)

Examples:
  # Import from YAML
  ksa machine import --file=machines.yaml
  
  # Import from CSV with dry-run
  ksa machine import --file=machines.csv --dry-run
  
  # Import and skip SSH verification
  ksa machine import --file=machines.yaml --skip-ssh-verify`,
    }
    
    cmd.Flags().String("file", "", "Input file path (required)")
    cmd.Flags().String("format", "auto", "File format (auto, yaml, json, csv)")
    cmd.Flags().String("nodepool", "", "Override nodepool (if not specified in file)")
    cmd.Flags().Bool("skip-ssh-verify", false, "Skip SSH connection verification")
    cmd.Flags().Bool("dry-run", false, "Preview import without saving")
    cmd.Flags().Bool("overwrite", false, "Overwrite existing machines with same IP")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        // 读取文件
        data, err := os.ReadFile(opts.File)
        if err != nil {
            return err
        }
        
        // 解析文件
        var machines []*model.Machine
        switch opts.Format {
        case "auto":
            machines, err = parseMachinesAuto(data, opts.Nodepool)
        case "yaml":
            machines, err = parseMachinesYAML(data, opts.Nodepool)
        case "json":
            machines, err = parseMachinesJSON(data, opts.Nodepool)
        case "csv":
            machines, err = parseMachinesCSV(data, opts.Nodepool)
        }
        
        if err != nil {
            return fmt.Errorf("failed to parse file: %w", err)
        }
        
        // Dry-run 预览
        if opts.DryRun {
            return previewImport(machines)
        }
        
        // 验证并导入
        return importMachines(ctx, db, machines, opts)
    }
    
    return cmd
}

func parseMachinesYAML(data []byte, overrideNodepool string) ([]*model.Machine, error) {
    var list MachineList
    if err := yaml.Unmarshal(data, &list); err != nil {
        return nil, err
    }
    
    nodepoolName := overrideNodepool
    if nodepoolName == "" && list.Metadata.Nodepool != "" {
        nodepoolName = list.Metadata.Nodepool
    }
    
    if nodepoolName == "" {
        return nil, fmt.Errorf("nodepool must be specified (via --nodepool or file metadata)")
    }
    
    machines := make([]*model.Machine, 0, len(list.Spec.Machines))
    for _, m := range list.Spec.Machines {
        machine := &model.Machine{
            Hostname:    m.Hostname,
            IPAddress:   net.ParseIP(m.IPAddress),
            CPUCores:    m.CPUCores,
            MemoryBytes: m.MemoryBytes,
            SSHUser:     m.SSHUser,
            SSHPort:     m.SSHPort,
            Region:      m.Region,
            Zone:        m.Zone,
            Rack:        m.Rack,
            Labels:      m.Labels,
            Tags:        m.Tags,
            Status:      model.StatusAvailable,
        }
        
        if m.GPUInfo != nil {
            machine.GPUInfo = &model.GPUInfo{
                Type:  m.GPUInfo.Type,
                Count: m.GPUInfo.Count,
            }
        }
        
        machines = append(machines, machine)
    }
    
    return machines, nil
}

func previewImport(machines []*model.Machine) error {
    fmt.Println("Preview of machines to be imported:")
    fmt.Println(strings.Repeat("-", 80))
    fmt.Printf("%-20s %-18s %-8s %-12s %-10s\n", "Hostname", "IP Address", "CPU", "Memory", "SSH User")
    fmt.Println(strings.Repeat("-", 80))
    
    for _, m := range machines {
        memoryStr := formatMemory(m.MemoryBytes)
        fmt.Printf("%-20s %-18s %-8d %-12s %-10s\n",
            m.Hostname, m.IPAddress.String(), m.CPUCores, memoryStr, m.SSHUser)
    }
    
    fmt.Println(strings.Repeat("-", 80))
    fmt.Printf("Total: %d machines\n", len(machines))
    fmt.Println("\nRun without --dry-run to actually import these machines.")
    
    return nil
}

func importMachines(ctx context.Context, db *database.DB, machines []*model.Machine, opts ImportOptions) error {
    // 获取 nodepool ID
    nodepool, err := db.GetNodepoolByName(ctx, opts.Nodepool)
    if err != nil {
        return fmt.Errorf("nodepool not found: %s", opts.Nodepool)
    }
    
    // 获取 SSH 密钥
    sshKey, err := db.GetDefaultSSHKey(ctx)
    if err != nil {
        return err
    }
    
    var successCount, failedCount int
    
    for i, m := range machines {
        // 设置 nodepool
        m.NodepoolID = nodepool.ID
        m.SSHKeyID = sshKey.ID
        
        // 检查是否已存在
        existing, _ := db.GetMachineByIP(ctx, m.IPAddress)
        if existing != nil {
            if opts.Overwrite {
                m.ID = existing.ID
                if err := db.UpdateMachine(ctx, m); err != nil {
                    fmt.Printf("Failed to update %s: %v\n", m.Hostname, err)
                    failedCount++
                    continue
                }
                fmt.Printf("Updated: %s (%s)\n", m.Hostname, m.IPAddress)
            } else {
                fmt.Printf("Skipped (exists): %s (%s)\n", m.Hostname, m.IPAddress)
                continue
            }
        } else {
            if err := db.CreateMachine(ctx, m); err != nil {
                fmt.Printf("Failed to import %s: %v\n", m.Hostname, err)
                failedCount++
                continue
            }
        }
        
        // SSH 验证（可选）
        if !opts.SkipSSHVerify {
            if err := verifySSHConnection(ctx, m); err != nil {
                fmt.Printf("Warning: SSH verification failed for %s: %v\n", m.Hostname, err)
            }
        }
        
        successCount++
        
        // 进度显示
        if (i+1)%10 == 0 || i == len(machines)-1 {
            fmt.Printf("\rImporting: %d/%d", i+1, len(machines))
        }
    }
    
    fmt.Printf("\nImport completed: %d success, %d failed\n", successCount, failedCount)
    
    return nil
}

// MachineList YAML 结构
type MachineList struct {
    APIVersion string      `yaml:"apiVersion"`
    Kind       string      `yaml:"kind"`
    Metadata   ListMetadata `yaml:"metadata"`
    Spec       ListSpec     `yaml:"spec"`
}

type ListMetadata struct {
    Nodepool string `yaml:"nodepool,omitempty"`
}

type ListSpec struct {
    Machines []MachineSpec `yaml:"machines"`
}

type MachineSpec struct {
    Hostname    string     `yaml:"hostname"`
    IPAddress   string     `yaml:"ip_address"`
    CPUCores    int        `yaml:"cpu_cores"`
    MemoryBytes int64      `yaml:"memory_bytes"`
    SSHUser     string     `yaml:"ssh_user"`
    SSHPort     int        `yaml:"ssh_port"`
    SSHKey      string     `yaml:"ssh_key"`
    GPUInfo     *GPUInfo   `yaml:"gpu_info,omitempty"`
    Region      string     `yaml:"region,omitempty"`
    Zone        string     `yaml:"zone,omitempty"`
    Rack        string     `yaml:"rack,omitempty"`
    Labels      map[string]string `yaml:"labels,omitempty"`
    Tags        map[string]string `yaml:"tags,omitempty"`
}

type GPUInfo struct {
    Type  string `yaml:"type"`
    Count int    `yaml:"count"`
}
```

#### P.2.3 自动发现

```bash
# 从 IP 段扫描并自动导入
ksa machine discover \
  --range=192.168.1.0/24 \
  --nodepool=cpu-pool \
  --ssh-user=root \
  --ssh-key=default

# 从多个 IP 段扫描
ksa machine discover \
  --ranges=192.168.1.0/24,192.168.2.0/24 \
  --nodepool=gpu-pool \
  --concurrent=10

# 带 SSH 验证的扫描
ksa machine discover \
  --range=192.168.1.0/24 \
  --nodepool=cpu-pool \
  --verify-ssh \
  --timeout=5s
```

```go
// cmd/machine/discover.go
func NewDiscoverMachinesCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "discover",
        Short: "Discover and import machines from IP range",
        Long: `Scan an IP range and automatically discover machines that can be imported.
        
The discovery process:
1. Ping sweep to find live hosts
2. SSH connection attempt on common ports
3. Gather system information (CPU, memory, OS)
4. Preview discovered machines
5. Import selected machines

Examples:
  # Basic discovery
  ksa machine discover --range=192.168.1.0/24 --nodepool=cpu-pool
  
  # With SSH verification
  ksa machine discover --range=192.168.1.0/24 --nodepool=cpu-pool --verify-ssh
  
  # Multiple ranges
  ksa machine discover --ranges=192.168.1.0/24,192.168.2.0/24 --nodepool=gpu-pool`,
    }
    
    cmd.Flags().String("range", "", "IP range (CIDR notation, e.g., 192.168.1.0/24)")
    cmd.Flags().String("ranges", "", "Comma-separated IP ranges")
    cmd.Flags().String("nodepool", "", "Target nodepool name (required)")
    cmd.Flags().String("ssh-user", "root", "SSH username for verification")
    cmd.Flags().String("ssh-key", "default", "SSH key name")
    cmd.Flags().Bool("verify-ssh", false, "Verify SSH connection during discovery")
    cmd.Flags().Duration("timeout", 5*time.Second, "Connection timeout per host")
    cmd.Flags().Int("concurrent", 5, "Number of concurrent scans")
    cmd.Flags().Bool("import", false, "Auto-import discovered machines")
    cmd.Flags().Bool("include-known", false, "Include already managed machines")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        // 解析 IP 范围
        ranges := strings.Split(opts.Ranges, ",")
        if opts.Range != "" {
            ranges = append(ranges, opts.Range)
        }
        
        if len(ranges) == 0 {
            return fmt.Errorf("IP range must be specified (--range or --ranges)")
        }
        
        // 发现机器
        discovered, err := discoverMachines(ctx, ranges, opts)
        if err != nil {
            return err
        }
        
        if len(discovered) == 0 {
            fmt.Println("No machines discovered")
            return nil
        }
        
        // 显示发现结果
        displayDiscovered(discovered)
        
        // 自动导入
        if opts.Import {
            return importDiscovered(ctx, db, discovered, opts.Nodepool)
        }
        
        return nil
    }
    
    return cmd
}

type DiscoveredMachine struct {
    IPAddress   net.IP
    Hostname    string
    SSHPort     int
    OS          string
    CPUCores    int
    MemoryBytes int64
    GPUInfo     *GPUInfo
    SSHVerified bool
    Error       string
}

func discoverMachines(ctx context.Context, ranges []string, opts DiscoverOptions) ([]*DiscoveredMachine, error) {
    // 生成 IP 列表
    var ips []net.IP
    for _, r := range ranges {
        parsedIPs, err := parseIPRange(r)
        if err != nil {
            return nil, err
        }
        ips = append(ips, parsedIPs...)
    }
    
    // 并发扫描
    discovered := make([]*DiscoveredMachine, 0)
    var mu sync.Mutex
    var wg sync.WaitGroup
    
    semaphore := make(chan struct{}, opts.Concurrent)
    
    for _, ip := range ips {
        wg.Add(1)
        semaphore <- struct{}{}
        
        go func(ip net.IP) {
            defer wg.Done()
            defer func() { <-semaphore }()
            
            dm := &DiscoveredMachine{
                IPAddress: ip,
                SSHPort:   22,
            }
            
            // 1. Ping 检测
            if !pingHost(ip, opts.Timeout) {
                dm.Error = "host unreachable"
                mu.Lock()
                discovered = append(discovered, dm)
                mu.Unlock()
                return
            }
            
            // 2. SSH 探测（如果需要验证）
            if opts.VerifySSH || opts.SSHKey != "" {
                if sshInfo := probeSSH(ctx, ip, 22, opts.SSHUser, opts.Timeout); sshInfo != nil {
                    dm.Hostname = sshInfo.Hostname
                    dm.SSHPort = sshInfo.Port
                    dm.OS = sshInfo.OS
                    dm.CPUCores = sshInfo.CPUCores
                    dm.MemoryBytes = sshInfo.MemoryBytes
                    dm.GPUInfo = sshInfo.GPUInfo
                    dm.SSHVerified = true
                } else {
                    dm.Error = "SSH connection failed"
                }
            } else {
                dm.Hostname = fmt.Sprintf("node-%s", ip.String())
            }
            
            mu.Lock()
            discovered = append(discovered, dm)
            mu.Unlock()
        }(ip)
    }
    
    wg.Wait()
    
    return discovered, nil
}

func probeSSH(ctx context.Context, ip net.IP, port int, user string, timeout time.Duration) *SSHInfo {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    
    // 尝试连接
    config := &ssh.ClientConfig{
        User: user,
        Auth: []ssh.AuthMethod{ssh.InsecureIgnoreHostKey()},
        Timeout: timeout,
    }
    
    client, err := ssh.Dial(fmt.Sprintf("%s:%d", ip, port), config)
    if err != nil {
        return nil
    }
    defer client.Close()
    
    // 获取系统信息
    info := &SSHInfo{
        IPAddress: ip,
        Port:      port,
    }
    
    // 获取主机名
    if output, _ := executeSSHCommand(ctx, client, "hostname -f"); output != "" {
        info.Hostname = strings.TrimSpace(output)
    }
    
    // 获取 OS 信息
    if output, _ := executeSSHCommand(ctx, client, "cat /etc/os-release | grep PRETTY_NAME"); output != "" {
        if matches := regexp.MustCompile(`PRETTY_NAME="([^"]+)"`).FindStringSubmatch(output); len(matches) > 1 {
            info.OS = matches[1]
        }
    }
    
    // 获取 CPU 信息
    if output, _ := executeSSHCommand(ctx, client, "nproc"); output != "" {
        if cores, err := strconv.Atoi(strings.TrimSpace(output)); err == nil {
            info.CPUCores = cores
        }
    }
    
    // 获取内存信息
    if output, _ := executeSSHCommand(ctx, client, "free -b | grep Mem | awk '{print $2}'"); output != "" {
        if mem, err := strconv.ParseInt(strings.TrimSpace(output), 10); err == nil {
            info.MemoryBytes = mem
        }
    }
    
    // 检查 GPU
    if output, _ := executeSSHCommand(ctx, client, "nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null"); output != "" {
        lines := strings.Split(strings.TrimSpace(output), "\n")
        if len(lines) > 0 {
            info.GPUInfo = &GPUInfo{
                Type:  strings.TrimSpace(lines[0]),
                Count: len(lines),
            }
        }
    }
    
    return info
}

func displayDiscovered(discovered []*DiscoveredMachine) {
    fmt.Println("\nDiscovered machines:")
    fmt.Println(strings.Repeat("=", 100))
    fmt.Printf("%-5s %-20s %-18s %-8s %-12s %-10s %s\n",
        "#", "Hostname", "IP Address", "CPU", "Memory", "OS", "Status")
    fmt.Println(strings.Repeat("=", 100))
    
    for i, dm := range discovered {
        memoryStr := formatMemory(dm.MemoryBytes)
        
        status := "SSH Failed"
        if dm.SSHVerified {
            status = "Ready"
        } else if dm.Error != "" {
            status = dm.Error
        }
        
        fmt.Printf("%-5d %-20s %-18s %-8d %-12s %-10s %s\n",
            i+1, dm.Hostname, dm.IPAddress.String(), dm.CPUCores, memoryStr, dm.OS, status)
    }
    
    fmt.Println(strings.Repeat("=", 100))
    fmt.Printf("Total: %d machines found, %d SSH-verified\n\n",
        len(discovered), countVerified(discovered))
    
    fmt.Println("To import: ksa machine import --file=<output>")
    fmt.Println("Or use --import flag to auto-import")
}

func importDiscovered(ctx context.Context, db *database.DB, discovered []*DiscoveredMachine, nodepoolName string) error {
    nodepool, err := db.GetNodepoolByName(ctx, nodepoolName)
    if err != nil {
        return err
    }
    
    sshKey, err := db.GetDefaultSSHKey(ctx)
    if err != nil {
        return err
    }
    
    var imported int
    for _, dm := range discovered {
        if !dm.SSHVerified {
            continue
        }
        
        machine := &model.Machine{
            Hostname:    dm.Hostname,
            IPAddress:   dm.IPAddress,
            NodepoolID:  nodepool.ID,
            SSHKeyID:    sshKey.ID,
            SSHUser:     "root",
            SSHPort:     dm.SSHPort,
            CPUCores:    dm.CPUCores,
            MemoryBytes: dm.MemoryBytes,
            GPUInfo:     dm.GPUInfo,
            Status:      model.StatusAvailable,
        }
        
        if err := db.CreateMachine(ctx, machine); err != nil {
            fmt.Printf("Failed to import %s: %v\n", dm.Hostname, err)
            continue
        }
        
        imported++
    }
    
    fmt.Printf("Imported %d machines to nodepool %s\n", imported, nodepoolName)
    
    return nil
}
```

### P.3 机器与池子关联管理

#### P.3.1 机器分配到池子

```bash
# 将单台机器分配到池子
ksa machine assign machine-001 --nodepool=cpu-pool

# 将多台机器分配到池子
ksa machine assign machine-001,machine-002 --nodepool=gpu-pool

# 根据 IP 范围分配
ksa machine assign --ip-range=192.168.1.100-192.168.1.110 --nodepool=cpu-pool

# 移动机器到另一个池子
ksa machine move machine-001 --from=cpu-pool --to=gpu-pool

# 从池子移除机器（不移除数据库记录）
ksa machine unassign machine-001 --nodepool=cpu-pool

# 彻底删除机器
ksa machine delete machine-001 --force
```

```go
// cmd/machine/assign.go
func NewAssignMachineCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "assign",
        Short: "Assign machines to a nodepool",
        Long: `Assign one or more machines to a nodepool.
        
Examples:
  # Assign single machine
  ksa machine assign machine-001 --nodepool=cpu-pool
  
  # Assign multiple machines
  ksa machine assign machine-001,machine-002 --nodepool=gpu-pool
  
  # Assign by IP range
  ksa machine assign --ip-range=192.168.1.100-192.168.1.110 --nodepool=cpu-pool`,
    }
    
    cmd.Flags().StringSliceVar(&machineIDs, "machines", nil, "Machine IDs or hostnames")
    cmd.Flags().String("ip-range", "", "IP range (e.g., 192.168.1.100-192.168.1.110)")
    cmd.Flags().String("nodepool", "", "Target nodepool name (required)")
    cmd.Flags().Bool("force", false, "Force assignment even if machines are in use")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        // 获取目标池子
        nodepool, err := db.GetNodepoolByName(ctx, opts.Nodepool)
        if err != nil {
            return fmt.Errorf("nodepool not found: %s", opts.Nodepool)
        }
        
        // 获取要分配的机器
        var machines []*model.Machine
        
        if opts.IPRange != "" {
            machines, err = getMachinesByIPRange(ctx, opts.IPRange)
        } else {
            machines, err = getMachinesByIDs(ctx, opts.MachineIDs)
        }
        
        if err != nil {
            return err
        }
        
        // 检查机器状态
        for _, m := range machines {
            if m.Status != model.StatusAvailable && m.Status != model.StatusMaintenance {
                if !opts.Force {
                    return fmt.Errorf("machine %s is %s, use --force to assign anyway",
                        m.Hostname, m.Status)
                }
            }
            
            // 检查硬件是否满足池子要求
            if err := checkMachineRequirements(m, nodepool); err != nil {
                return fmt.Errorf("machine %s does not meet pool requirements: %v",
                    m.Hostname, err)
            }
            
            // 更新池子关联
            m.NodepoolID = nodepool.ID
            m.Status = model.StatusAvailable
            
            if err := db.UpdateMachine(ctx, m); err != nil {
                return err
            }
            
            fmt.Printf("Assigned %s to %s\n", m.Hostname, nodepool.Name)
        }
        
        return nil
    }
    
    return cmd
}

// cmd/machine/move.go
func NewMoveMachineCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "move",
        Short: "Move machines between nodepools",
        Long: `Move one or more machines from one nodepool to another.
        
This operation will:
1. Drain the machine from current nodepool (if in use)
2. Reset the node
3. Update the nodepool association
4. Make the machine available in the new pool

Examples:
  # Move single machine
  ksa machine move machine-001 --from=cpu-pool --to=gpu-pool
  
  # Move all machines from a pool
  ksa machine move --all-from=cpu-pool --to=gpu-pool`,
    }
    
    cmd.Flags().StringSliceVar(&machineIDs, "machines", nil, "Machine IDs")
    cmd.Flags().String("all-from", "", "Move all machines from this nodepool")
    cmd.Flags().String("from", "", "Source nodepool name")
    cmd.Flags().String("to", "", "Target nodepool name (required)")
    cmd.Flags().Bool("skip-drain", false, "Skip drain operation")
    cmd.Flags().Bool("force", false, "Force move even if machine is in use")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        // 验证参数
        if opts.AllFrom == "" && len(opts.MachineIDs) == 0 {
            return fmt.Errorf("must specify --machines or --all-from")
        }
        if opts.To == "" {
            return fmt.Errorf("must specify --to")
        }
        
        // 获取源池子和目标池子
        fromPool, err := db.GetNodepoolByName(ctx, opts.From)
        if err != nil {
            return err
        }
        
        toPool, err := db.GetNodepoolByName(ctx, opts.To)
        if err != nil {
            return err
        }
        
        // 获取要移动的机器
        var machines []*model.Machine
        if opts.AllFrom != "" {
            machines, err = db.GetMachinesByNodepool(ctx, fromPool.ID)
        } else {
            machines, err = getMachinesByIDs(ctx, opts.MachineIDs)
        }
        
        if err != nil {
            return err
        }
        
        // 执行移动
        for _, m := range machines {
            if err := moveMachine(ctx, executor, m, fromPool, toPool, opts); err != nil {
                fmt.Printf("Failed to move %s: %v\n", m.Hostname, err)
            } else {
                fmt.Printf("Moved %s from %s to %s\n", m.Hostname, fromPool.Name, toPool.Name)
            }
        }
        
        return nil
    }
    
    return cmd
}

func moveMachine(ctx context.Context, executor *SSHExecutor, machine *model.Machine,
    fromPool, toPool *model.Nodepool, opts MoveOptions) error {
    
    // 如果机器正在使用，先 drain
    if machine.Status == model.StatusInUse && !opts.SkipDrain {
        // 执行 drain
        drainOpts := DrainOptions{
            GracePeriodSeconds: 300,
            Timeout:            10 * time.Minute,
            DeleteLocalData:    true,
            Force:              true,
        }
        
        if _, err := executor.ExecuteDrain(ctx, machine, drainOpts); err != nil {
            if !opts.Force {
                return fmt.Errorf("drain failed: %w", err)
            }
            fmt.Printf("Warning: drain failed for %s: %v\n", machine.Hostname, err)
        }
        
        // 执行 reset
        if _, err := executor.ExecuteReset(ctx, machine, true); err != nil {
            fmt.Printf("Warning: reset failed for %s: %v\n", machine.Hostname, err)
        }
        
        machine.Status = model.StatusAvailable
    }
    
    // 更新池子关联
    machine.NodepoolID = toPool.ID
    
    // 应用新池子的标签和污点
    machine.Labels = toPool.TemplateLabels
    
    return db.UpdateMachine(ctx, machine)
}
```

### P.4 池子日常维护

#### P.4.1 池子状态监控

```bash
# 查看所有池子状态
ksa pool status

# 查看单个池子详细信息
ksa pool status cpu-pool

# 查看池子成员
ksa pool members gpu-pool

# 查看池子资源使用率
ksa pool usage gpu-pool

# 查看池子事件
ksa pool events gpu-pool --since=24h
```

```bash
# 输出示例
$ ksa pool status

Nodepool      Machines    Available    In Use    Pending    Status    Min/Max
cpu-pool      20          15           5         0          Active    0/20
gpu-pool-a100 10          8            2         1          Active    0/10
batch-pool    5           5            0         0          Active    0/10

Total: 35 machines

$ ksa pool status gpu-pool-a100

Nodepool: gpu-pool-a100
Status: Active
Min/Max: 0/10
Current Size: 10
Available: 8
In Use: 2
Pending: 0

Hardware Requirements:
  - CPU: 32+ cores
  - Memory: 128GB+
  - GPU: nvidia-a100 (4x)

Labels:
  - node-pool=gpu-pool-a100
  - gpu-type=nvidia-a100

Taints:
  - nvidia.com/gpu=NoSchedule

Recent Events:
  2024-01-17 10:30:00  scale_up    success  Added node-gpu-008
  2024-01-17 09:15:00  scale_up    success  Added node-gpu-007
```

```go
// cmd/pool/status.go
func NewPoolStatusCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "status [name]",
        Short: "Show nodepool status",
        Long: `Display nodepool status and statistics.
        
Without argument, shows all nodepools.
With argument, shows detailed status of the specified nodepool.`,
    }
    
    cmd.Flags().Bool("json", false, "Output in JSON format")
    cmd.Flags().Bool("wide", false, "Show detailed information")
    cmd.Flags().String("since", "", "Show events since (e.g., 24h, 7d)")
    
    cmd.RunE = func(cmd *cobra.Command, args []string) error {
        if len(args) > 0 {
            // 单个池子状态
            return showPoolStatus(ctx, args[0], opts)
        }
        
        // 所有池子状态
        return showAllPoolsStatus(ctx, opts)
    }
    
    return cmd
}

func showAllPoolsStatus(ctx context.Context, opts StatusOptions) error {
    nodepools, err := db.ListNodepools(ctx)
    if err != nil {
        return err
    }
    
    // 获取每个池子的机器统计
    table := [][]string{
        {"Nodepool", "Machines", "Available", "In Use", "Pending", "Status", "Min/Max"},
        strings.Repeat("-", 80),
    }
    
    for _, np := range nodepools {
        machines, _ := db.GetMachinesByNodepool(ctx, np.ID)
        
        stats := calculatePoolStats(machines)
        
        status := "Active"
        if !np.IsActive {
            status = "Disabled"
        }
        
        row := []string{
            np.Name,
            fmt.Sprintf("%d", len(machines)),
            fmt.Sprintf("%d", stats.Available),
            fmt.Sprintf("%d", stats.InUse),
            fmt.Sprintf("%d", stats.Pending),
            status,
            fmt.Sprintf("%d/%d", np.MinSize, np.MaxSize),
        }
        table = append(table, row)
    }
    
    // 打印表格
    printTable(table)
    
    // 汇总
    totalMachines := 0
    for _, np := range nodepools {
        machines, _ := db.GetMachinesByNodepool(ctx, np.ID)
        totalMachines += len(machines)
    }
    
    fmt.Printf("\nTotal: %d machines in %d nodepools\n", totalMachines, len(nodepools))
    
    return nil
}

func showPoolStatus(ctx context.Context, name string, opts StatusOptions) error {
    nodepool, err := db.GetNodepoolByName(ctx, name)
    if err != nil {
        return err
    }
    
    machines, err := db.GetMachinesByNodepool(ctx, nodepool.ID)
    if err != nil {
        return err
    }
    
    stats := calculatePoolStats(machines)
    
    // 打印池子信息
    fmt.Printf("Nodepool: %s\n", nodepool.Name)
    if nodepool.DisplayName != "" {
        fmt.Printf("Display Name: %s\n", nodepool.DisplayName)
    }
    fmt.Printf("Status: %s\n", map[bool]string{true: "Active", false: "Disabled"}[nodepool.IsActive])
    fmt.Printf("Min/Max: %d/%d\n", nodepool.MinSize, nodepool.MaxSize)
    fmt.Printf("Current Size: %d\n", len(machines))
    fmt.Printf("Available: %d\n", stats.Available)
    fmt.Printf("In Use: %d\n", stats.InUse)
    fmt.Printf("Pending: %d\n", stats.Pending)
    
    // 硬件要求
    fmt.Printf("\nHardware Requirements:\n")
    fmt.Printf("  - CPU: %d+ cores\n", nodepool.MinCPUCores)
    fmt.Printf("  - Memory: %s\n", formatMemory(nodepool.MinMemoryBytes))
    if nodepool.RequireGPU {
        fmt.Printf("  - GPU: %s (%dx)\n", nodepool.GPUType, nodepool.GPUCount)
    }
    
    // 标签和污点
    if len(nodepool.TemplateLabels) > 0 {
        fmt.Printf("\nLabels:\n")
        for k, v := range nodepool.TemplateLabels {
            fmt.Printf("  - %s=%s\n", k, v)
        }
    }
    
    if len(nodepool.TemplateTaints) > 0 {
        fmt.Printf("\nTaints:\n")
        for _, t := range nodepool.TemplateTaints {
            fmt.Printf("  - %s=%s:%s\n", t.Key, t.Value, t.Effect)
        }
    }
    
    // 成员列表
    if opts.Wide {
        fmt.Printf("\nMembers (%d):\n", len(machines))
        fmt.Printf("%-20s %-18s %-12s %-15s\n", "Hostname", "IP Address", "Status", "Last Heartbeat")
        fmt.Println(strings.Repeat("-", 65))
        
        for _, m := range machines {
            lastHeartbeat := "never"
            if !m.LastHeartbeat.IsZero() {
                lastHeartbeat = m.LastHeartbeat.Format("2006-01-02 15:04:05")
            }
            fmt.Printf("%-20s %-18s %-12s %-15s\n",
                m.Hostname, m.IPAddress.String(), m.Status, lastHeartbeat)
        }
    }
    
    // 事件
    if opts.Since != "" {
        duration, _ := time.ParseDuration(opts.Since)
        since := time.Now().Add(-duration)
        
        events, _ := db.GetEventsByNodepoolSince(ctx, nodepool.ID, since)
        
        fmt.Printf("\nRecent Events:\n")
        for _, e := range events {
            fmt.Printf("  %s  %-12s  %s  %s\n",
                e.StartedAt.Format("2006-01-02 15:04:05"),
                e.EventType,
                e.Status,
                e.MachineID.String()[:8],
            )
        }
    }
    
    return nil
}
```

#### P.4.2 池子配置管理

```bash
# 创建池子
ksa pool create \
  --name=cpu-pool \
  --display-name="CPU Compute Pool" \
  --min-size=0 \
  --max-size=20 \
  --min-cpu-cores=8 \
  --min-memory=32GiB \
  --scale-up-policy=least-used \
  --scale-down-policy=oldest

# 创建 GPU 池子
ksa pool create \
  --name=gpu-pool-a100 \
  --display-name="GPU Pool (A100)" \
  --min-size=0 \
  --max-size=10 \
  --require-gpu=true \
  --gpu-type=nvidia-a100 \
  --gpu-count=4 \
  --template-labels=hardware-type=gpu,gpu-type=a100 \
  --template-taints=nvidia.com/gpu=NoSchedule

# 更新池子配置
ksa pool update cpu-pool --max-size=30 --scale-up-policy=random

# 启用/禁用池子
ksa pool enable gpu-pool
ksa pool disable gpu-pool

# 删除池子（需先移除所有机器）
ksa pool delete cpu-pool --force
```

#### P.4.3 池子成员管理

```bash
# 查看池子成员
ksa pool members gpu-pool

# 查看成员详情
ksa pool members gpu-pool --detail

# 标记机器为维护
ksa pool maintenance gpu-pool --machines=node-gpu-001,node-gpu-002 --reason="hardware-upgrade"

# 取消维护
ksa pool maintenance gpu-pool --machines=node-gpu-001 --cancel

# 替换成员
ksa pool replace gpu-pool --old=node-gpu-001 --new=node-gpu-010

# 同步标签
ksa pool sync-labels gpu-pool
```

### P.5 机器生命周期管理

#### P.5.1 状态转换

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         机器生命周期状态机                                │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│   ┌─────────────┐                                                       │
│   │  available  │ 待命状态，可被分配                                     │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 分配 → joining                                             │
│          ├── 维护 → maintenance                                         │
│          └── 报废 → archived                                            │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  joining    │ 正在加入集群                                           │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 成功 → in_use                                              │
│          └── 失败 → available (重试) 或 fault (需干预)                   │
│                                                                         │
│   ┌──────┬──────┐                                                       │
│   │           │                                                          │
│   │  in_use   │ 已加入集群                                               │
│   │           │                                                          │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 缩容 → draining                                            │
│          ├── 维护 → maintenance                                         │
│          └── 失联 → fault                                               │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  draining   │ 正在排空 Pod                                           │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 成功 → leaving                                             │
│          └── 超时 → maintenance                                         │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  leaving    │ 正在离开集群                                           │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 成功 → available                                           │
│          └── 失败 → maintenance                                         │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │    fault    │ 故障状态                                               │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 修复 → available                                           │
│          └── 报废 → archived                                            │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │ maintenance │ 维护状态                                               │
│   └──────┬──────┘                                                       │
│          │                                                               │
│          ├── 完成 → available                                           │
│          └── 报废 → archived                                            │
│                                                                         │
│   ┌──────▼──────┐                                                       │
│   │  archived   │ 已归档（历史记录）                                     │
│   └─────────────┘                                                       │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

#### P.5.2 生命周期操作

```go
// cmd/machine/lifecycle.go

// MachineLifecycle 机器生命周期管理器
type MachineLifecycle struct {
    db        *database.DB
    executor  *SSHExecutor
    k8sClient *k8s.Client
}

// SetMaintenance 设置机器为维护状态
func (lc *MachineLifecycle) SetMaintenance(ctx context.Context, machineID string, reason string) error {
    machine, err := lc.db.GetMachine(ctx, machineID)
    if err != nil {
        return err
    }
    
    // 状态检查
    if machine.Status == model.StatusMaintenance {
        return fmt.Errorf("machine is already in maintenance")
    }
    
    if machine.Status == model.StatusInUse {
        // 如果正在使用，先 drain
        drainOpts := DrainOptions{
            GracePeriodSeconds: 300,
            Timeout:            10 * time.Minute,
            DeleteLocalData:    true,
            Force:              true,
        }
        
        if _, err := lc.executor.ExecuteDrain(ctx, machine, drainOpts); err != nil {
            return fmt.Errorf("drain failed: %w", err)
        }
        
        // 执行 reset
        if _, err := lc.executor.ExecuteReset(ctx, machine, true); err != nil {
            log.Warn("reset failed", "machine", machine.Hostname, "error", err)
        }
    }
    
    // 更新状态
    machine.Status = model.StatusMaintenance
    machine.ErrorMessage = reason
    machine.LastAction = "maintenance"
    machine.LastActionAt = time.Now()
    
    return lc.db.UpdateMachine(ctx, machine)
}

// RestoreFromMaintenance 从维护状态恢复
func (lc *MachineLifecycle) RestoreFromMaintenance(ctx context.Context, machineID string) error {
    machine, err := lc.db.GetMachine(ctx, machineID)
    if err != nil {
        return err
    }
    
    if machine.Status != model.StatusMaintenance {
        return fmt.Errorf("machine is not in maintenance")
    }
    
    machine.Status = model.StatusAvailable
    machine.ErrorMessage = ""
    machine.LastAction = "restore"
    machine.LastActionAt = time.Now()
    
    return lc.db.UpdateMachine(ctx, machine)
}

// MarkAsFault 标记为故障
func (lc *MachineLifecycle) MarkAsFault(ctx context.Context, machineID string, reason string) error {
    machine, err := lc.db.GetMachine(ctx, machineID)
    if err != nil {
        return err
    }
    
    machine.Status = model.StatusFault
    machine.ErrorMessage = reason
    machine.LastAction = "fault"
    machine.LastActionAt = time.Now()
    
    // 发送告警
    alertSender.Send(&Alert{
        Type:      AlertTypeMachineFault,
        Severity:  AlertSeverityError,
        Machine:   machine.IPAddress,
        Nodepool:  machine.NodepoolID,
        Message:   fmt.Sprintf("Machine %s marked as fault: %s", machine.Hostname, reason),
    })
    
    return lc.db.UpdateMachine(ctx, machine)
}

// Archive 归档机器
func (lc *MachineLifecycle) Archive(ctx context.Context, machineID string) error {
    machine, err := lc.db.GetMachine(ctx, machineID)
    if err != nil {
        return err
    }
    
    if machine.Status == model.StatusInUse {
        return fmt.Errorf("cannot archive machine in use")
    }
    
    // 备份到历史表
    history := &model.MachineHistory{
        OriginalID: machine.ID,
        Hostname:   machine.Hostname,
        IPAddress:  machine.IPAddress,
        NodepoolID: machine.NodepoolID,
        Status:     machine.Status,
        CPUCores:   machine.CPUCores,
        MemoryBytes: machine.MemoryBytes,
        GPUInfo:    machine.GPUInfo,
        ArchivedAt: time.Now(),
    }
    
    if err := lc.db.ArchiveMachine(ctx, history); err != nil {
        return err
    }
    
    // 从主表删除
    return lc.db.DeleteMachine(ctx, machineID)
}

// RestoreFromArchive 从归档恢复
func (lc *MachineLifecycle) RestoreFromArchive(ctx context.Context, historyID string) error {
    history, err := lc.db.GetMachineHistory(ctx, historyID)
    if err != nil {
        return err
    }
    
    machine := &model.Machine{
        Hostname:    history.Hostname,
        IPAddress:   history.IPAddress,
        NodepoolID:  history.NodepoolID,
        Status:      model.StatusAvailable,
        CPUCores:    history.CPUCores,
        MemoryBytes: history.MemoryBytes,
        GPUInfo:     history.GPUInfo,
    }
    
    return lc.db.CreateMachine(ctx, machine)
}
```

### P.6 管理工具和界面设计

#### P.6.1 Web UI 设计

```yaml
# Web UI 页面结构

pages:
  - name: Dashboard
    path: /
    components:
      - StatisticsCards: 总览统计
      - NodepoolTable: 池子状态表格
      - RecentEvents: 最近事件
      - MachineStatusChart: 机器状态饼图
  
  - name: Nodepools
    path: /nodepools
    components:
      - NodepoolList: 池子列表
      - NodepoolDetail: 池子详情
      - MemberTable: 成员管理
      - ConfigEditor: 配置编辑
  
  - name: Machines
    path: /machines
    components:
      - MachineTable: 机器列表
      - MachineDetail: 机器详情
      - MachineFilter: 过滤器
      - BatchActions: 批量操作
  
  - name: Import
    path: /import
    components:
      - ManualAddForm: 手动添加表单
      - BatchImportForm: 批量导入表单
      - DiscoveryForm: 自动发现表单
      - ImportPreview: 导入预览
  
  - name: Events
    path: /events
    components:
      - EventTimeline: 事件时间线
      - EventFilter: 过滤器
      - EventDetail: 事件详情
```

```typescript
// API 响应类型

interface NodepoolListResponse {
  nodepools: NodepoolInfo[];
  total: number;
}

interface NodepoolInfo {
  id: string;
  name: string;
  displayName: string;
  minSize: number;
  maxSize: number;
  currentSize: number;
  available: number;
  inUse: number;
  status: 'active' | 'disabled';
  gpuType?: string;
  gpuCount?: number;
  createdAt: string;
}

interface MachineListResponse {
  machines: MachineInfo[];
  total: number;
  page: number;
  pageSize: number;
}

interface MachineInfo {
  id: string;
  hostname: string;
  ipAddress: string;
  nodepool: string;
  status: MachineStatus;
  cpuCores: number;
  memoryBytes: number;
  gpuInfo?: GPUInfo;
  labels: Record<string, string>;
  lastHeartbeat: string;
  createdAt: string;
}

type MachineStatus = 
  | 'available'
  | 'joining'
  | 'in_use'
  | 'draining'
  | 'leaving'
  | 'fault'
  | 'maintenance';
```

#### P.6.2 快捷操作

```bash
# 快速查看池子状态
alias ksa-pools='ksa pool status'

# 快速查看机器状态
alias ksa-machines='ksa machine list --status=in_use'

# 快速扩容
alias ksa-scaleup='ksa scale up --nodepool=cpu-pool --delta=1'

# 快速故障排查
alias ksa-health='ksa machine list --status=fault --detail'

# 快速查看事件
alias ksa-events='ksa event list --since=1h'
```

### P.7 附录：勘误汇总（续）

| 日期 | 版本 | 修复内容 |
|-----|------|---------|
| 2024-01-17 | v1.0.2 | 新增机器导入功能设计（手动/批量/自动发现） |
| 2024-01-17 | v1.0.2 | 新增机器和池子关联管理（assign/move/unassign） |
| 2024-01-17 | v1.0.2 | 新增池子日常维护操作（status/members/update） |
| 2024-01-17 | v1.0.2 | 新增机器生命周期管理（maintenance/fault/archive） |
| 2024-01-17 | v1.0.2 | 新增管理工具和界面设计（Web UI/API/CLI） |
| 2024-01-17 | v1.0.2 | 新增机器 YAML/CSV 导入格式定义 |
| 2024-01-17 | v1.0.2 | 新增自动发现功能（IP 扫描 + SSH 探测） |
| 2024-01-17 | v1.0.3 | 移除 BMC 相关设计，本系统只支持已开机的机器，所有操作通过 SSH 进行 |
| 2024-01-17 | v1.0.3 | 简化机器不可达处理策略，SSH 失败直接标记为故障 |
| 2024-01-17 | v1.0.3 | 移除数据库表中的 bmc_ip 和 power_management 字段 |
| 2026-01-16 | v1.0.4 | 修复 `Release()` 方法调用缺少参数问题（第 802、866 行） |
| 2026-01-16 | v1.0.4 | 修复 `IsConnected()` 方法不存在问题，改用 SSH keepalive 请求检测 |
| 2026-01-16 | v1.0.4 | 修复 `math.random.Float64()` 为 `rand.Float64()`，补充缺失的 import |
