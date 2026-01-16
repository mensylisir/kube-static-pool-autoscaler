/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package commands

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"kube-static-pool-autoscaler/internal/config"
	"kube-static-pool-autoscaler/internal/db"
	"kube-static-pool-autoscaler/internal/models"

	"github.com/pelletier/go-toml/v2"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// RootCommand is the root command for the CLI
type RootCommand struct {
	cmd          *cobra.Command
	cfg          *config.Config
	db           *db.DB
	logger       *logrus.Entry
	machineRepo  *db.MachineRepository
	nodepoolRepo *db.NodepoolRepository
}

// SetArgs sets the arguments for the command
func (r *RootCommand) SetArgs(args []string) {
	r.cmd.SetArgs(args)
}

// NewRootCommand creates a new root command
func NewRootCommand(cfg *config.Config, database *db.DB, logger *logrus.Entry) *RootCommand {
	cmd := &cobra.Command{
		Use:   "ksa",
		Short: "Kube Static Pool Autoscaler CLI",
		Long: `Kube Static Pool Autoscaler CLI for managing static node pools
in Kubernetes clusters with pre-provisioned physical/virtual machines.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	root := &RootCommand{
		cmd:    cmd,
		cfg:    cfg,
		db:     database,
		logger: logger,
	}

	if database != nil {
		root.machineRepo = db.NewMachineRepository(database)
		root.nodepoolRepo = db.NewNodepoolRepository(database)
	}

	cmd.AddCommand(
		newVersionCommand(),
		newMachineCommand(root),
		newNodePoolCommand(root),
		newDiscoveryCommand(root),
	)

	cmd.PersistentFlags().Bool("verbose", false, "Enable verbose output")
	cmd.PersistentFlags().String("config", "", "Path to config file")

	return root
}

func (r *RootCommand) ExecuteContext(ctx context.Context) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		os.Exit(1)
	}()

	return r.cmd.ExecuteContext(ctx)
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Long:  "Print the version number and build information",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("Kube Static Pool Autoscaler CLI")
			cmd.Println("Version: dev")
		},
	}
}

type MachineCommand struct {
	root *RootCommand
}

func newMachineCommand(root *RootCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "machine",
		Short: "Manage machines",
		Long:  "Commands for adding, listing, and managing machines in the static pool",
	}

	m := &MachineCommand{root: root}

	cmd.AddCommand(
		newMachineAddCommand(m),
		newMachineListCommand(m),
		newMachineImportCommand(m),
		newMachineDeleteCommand(m),
		newMachineStatusCommand(m),
	)

	return cmd
}

func newMachineAddCommand(m *MachineCommand) *cobra.Command {
	var sshPort int
	var sshKeyPath string
	var labels string

	cmd := &cobra.Command{
		Use:   "add [name] [ip] [ssh_user]",
		Short: "Add a new machine to the pool",
		Long: `Add a new machine to the static pool.
[name] is an optional identifier for the machine (defaults to IP if not provided).
[ip] is the IP address or hostname of the machine.
[ssh_user] is the SSH username for connecting to the machine.`,
		Args: cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			name := args[0]
			ip := args[1]
			user := args[2]

			machine := &models.Machine{
				Name:       name,
				IPAddress:  ip,
				SSHUser:    user,
				SSHPort:    sshPort,
				SSHKeyPath: sshKeyPath,
				State:      models.MachineStateAvailable,
				Labels:     parseLabels(labels),
			}

			if err := m.root.machineRepo.Create(ctx, machine); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully added machine %s (%s)\n", machine.ID, machine.IPAddress)
		},
	}

	cmd.Flags().IntVarP(&sshPort, "port", "p", 22, "SSH port")
	cmd.Flags().StringVarP(&sshKeyPath, "key", "k", "", "Path to SSH private key")
	cmd.Flags().StringVarP(&labels, "labels", "l", "", "Labels as key=value pairs (comma-separated)")

	return cmd
}

func newMachineListCommand(m *MachineCommand) *cobra.Command {
	var jsonOutput bool
	var state string
	var nodepool string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all machines in the pool",
		Long:  "List all machines currently in the static pool",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			machines, err := m.root.machineRepo.GetAll(ctx, 1000, 0)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			// Filter by state
			if state != "" {
				filtered := make([]*models.Machine, 0)
				for _, m := range machines {
					if string(m.State) == state {
						filtered = append(filtered, m)
					}
				}
				machines = filtered
			}

			// Filter by nodepool
			if nodepool != "" {
				filtered := make([]*models.Machine, 0)
				for _, m := range machines {
					if m.NodepoolID.Valid && m.NodepoolID.String == nodepool {
						filtered = append(filtered, m)
					}
				}
				machines = filtered
			}

			if jsonOutput {
				data, _ := json.MarshalIndent(machines, "", "  ")
				cmd.Println(string(data))
				return
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tName\tIP\tState\tNodepool\tLabels")
			fmt.Fprintln(w, "----\t----\t--\t-----\t--------\t------")
			for _, machine := range machines {
				nodepoolID := ""
				if machine.NodepoolID.Valid {
					nodepoolID = machine.NodepoolID.String
				}
				labels := formatLabels(machine.Labels)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					machine.ID[:8], machine.Name, machine.IPAddress, machine.State, nodepoolID, labels)
			}
			w.Flush()
		},
	}

	cmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON")
	cmd.Flags().StringVarP(&state, "state", "s", "", "Filter by state")
	cmd.Flags().StringVarP(&nodepool, "nodepool", "n", "", "Filter by nodepool ID")

	return cmd
}

func newMachineImportCommand(m *MachineCommand) *cobra.Command {
	var file string
	var format string

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import machines from a file",
		Long:  "Import multiple machines from a YAML, JSON, or CSV file",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			machines, err := parseMachineFile(file, format)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if err := m.root.machineRepo.BulkCreate(ctx, machines); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully imported %d machines\n", len(machines))
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to file to import (required)")
	cmd.Flags().StringVarP(&format, "format", "r", "auto", "File format (yaml, json, csv, auto)")

	cmd.MarkFlagRequired("file")

	return cmd
}

func newMachineDeleteCommand(m *MachineCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [name|ip|id]",
		Short: "Remove a machine from the pool",
		Long:  "Remove a machine from the static pool by name, IP address, or ID",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			identifier := args[0]

			var machine *models.Machine
			var err error

			// Try to find by ID first (8 chars or full)
			machine, err = m.root.machineRepo.GetByID(ctx, identifier)
			if err != nil || machine == nil {
				// Try by IP
				machine, err = m.root.machineRepo.GetByIP(ctx, identifier)
			}

			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if machine == nil {
				cmd.Printf("Error: machine not found: %s\n", identifier)
				os.Exit(1)
			}

			if err := m.root.machineRepo.Delete(ctx, machine.ID); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully deleted machine %s (%s)\n", machine.ID, machine.IPAddress)
		},
	}

	return cmd
}

func newMachineStatusCommand(m *MachineCommand) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status [name|ip|id]",
		Short: "Show status of a machine",
		Long:  "Show detailed status information for a specific machine",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			identifier := args[0]

			var machine *models.Machine
			var err error

			machine, err = m.root.machineRepo.GetByID(ctx, identifier)
			if err != nil || machine == nil {
				machine, err = m.root.machineRepo.GetByIP(ctx, identifier)
			}

			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if machine == nil {
				cmd.Printf("Error: machine not found: %s\n", identifier)
				os.Exit(1)
			}

			if jsonOutput {
				data, _ := json.MarshalIndent(machine, "", "  ")
				cmd.Println(string(data))
				return
			}

			cmd.Printf("Machine ID:   %s\n", machine.ID)
			cmd.Printf("Name:         %s\n", machine.Name)
			cmd.Printf("IP Address:   %s\n", machine.IPAddress)
			cmd.Printf("SSH User:     %s\n", machine.SSHUser)
			cmd.Printf("SSH Port:     %d\n", machine.SSHPort)
			cmd.Printf("State:        %s\n", machine.State)
			if machine.NodepoolID.Valid {
				cmd.Printf("Nodepool:     %s\n", machine.NodepoolID.String)
			} else {
				cmd.Printf("Nodepool:     (none)\n")
			}
			cmd.Printf("CPU:          %d\n", machine.CPU)
			cmd.Printf("Memory:       %d bytes\n", machine.Memory)
			if machine.GPU.Valid {
				cmd.Printf("GPU:          %s\n", machine.GPU.String)
			}
			if machine.GPUCount.Valid {
				cmd.Printf("GPU Count:    %d\n", machine.GPUCount.Int64)
			}
			cmd.Printf("Created:      %s\n", machine.CreatedAt)
			cmd.Printf("Updated:      %s\n", machine.UpdatedAt)
			cmd.Printf("Last Seen:    %s\n", machine.LastSeenAt)
		},
	}

	cmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON")

	return cmd
}

type NodePoolCommand struct {
	root *RootCommand
}

func newNodePoolCommand(root *RootCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodepool",
		Short: "Manage node pools",
		Long:  "Commands for creating, listing, and managing node pools",
	}

	np := &NodePoolCommand{root: root}

	cmd.AddCommand(
		newNodePoolCreateCommand(np),
		newNodePoolListCommand(np),
		newNodePoolUpdateCommand(np),
		newNodePoolDeleteCommand(np),
		newNodePoolAssignCommand(np),
		newNodePoolUnassignCommand(np),
	)

	return cmd
}

func newNodePoolCreateCommand(np *NodePoolCommand) *cobra.Command {
	var minSize int
	var maxSize int
	var gpuType string
	var gpuCount int
	var cpu int
	var memory int64
	var labels string

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new node pool",
		Long:  "Create a new node pool with specified hardware requirements",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			name := args[0]

			nodepool := &models.Nodepool{
				Name:    name,
				MinSize: minSize,
				MaxSize: maxSize,
				State:   models.NodepoolStateActive,
				Labels:  parseLabels(labels),
			}

			if cpu > 0 {
				nodepool.CPU = sql.NullInt64{Int64: int64(cpu), Valid: true}
			}
			if memory > 0 {
				nodepool.Memory = sql.NullInt64{Int64: memory, Valid: true}
			}
			if gpuType != "" {
				nodepool.GPU = sql.NullString{String: gpuType, Valid: true}
			}
			if gpuCount > 0 {
				nodepool.GPUCount = sql.NullInt64{Int64: int64(gpuCount), Valid: true}
			}

			if err := np.root.nodepoolRepo.Create(ctx, nodepool); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully created nodepool %s (%s)\n", nodepool.ID, nodepool.Name)
		},
	}

	cmd.Flags().IntVarP(&minSize, "min", "m", 0, "Minimum number of nodes")
	cmd.Flags().IntVarP(&maxSize, "max", "M", 10, "Maximum number of nodes")
	cmd.Flags().StringVar(&gpuType, "gpu", "", "GPU type requirement")
	cmd.Flags().IntVar(&gpuCount, "gpu-count", 0, "Number of GPUs required")
	cmd.Flags().IntVar(&cpu, "cpu", 0, "CPU requirement")
	cmd.Flags().Int64Var(&memory, "memory", 0, "Memory requirement in bytes")
	cmd.Flags().StringVar(&labels, "labels", "", "Labels as key=value pairs (comma-separated)")

	return cmd
}

func newNodePoolListCommand(np *NodePoolCommand) *cobra.Command {
	var jsonOutput bool
	var state string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all node pools",
		Long:  "List all node pools and their current status",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			nodepools, err := np.root.nodepoolRepo.GetAll(ctx, 1000, 0)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if state != "" {
				filtered := make([]*models.Nodepool, 0)
				for _, np := range nodepools {
					if string(np.State) == state {
						filtered = append(filtered, np)
					}
				}
				nodepools = filtered
			}

			if jsonOutput {
				data, _ := json.MarshalIndent(nodepools, "", "  ")
				cmd.Println(string(data))
				return
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tName\tMin\tMax\tSize\tState\tLabels")
			fmt.Fprintln(w, "----\t----\t---\t---\t----\t-----\t------")
			for _, pool := range nodepools {
				size, _ := np.root.nodepoolRepo.GetMachineCount(ctx, pool.ID)
				labels := formatLabels(pool.Labels)
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
					pool.ID[:8], pool.Name, pool.MinSize, pool.MaxSize, size, pool.State, labels)
			}
			w.Flush()
		},
	}

	cmd.Flags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON")
	cmd.Flags().StringVarP(&state, "state", "s", "", "Filter by state")

	return cmd
}

func newNodePoolUpdateCommand(np *NodePoolCommand) *cobra.Command {
	var minSize int
	var maxSize int
	var state string

	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update a node pool",
		Long:  "Update the configuration of an existing node pool",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			name := args[0]

			nodepool, err := np.root.nodepoolRepo.GetByName(ctx, name)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if nodepool == nil {
				cmd.Printf("Error: nodepool not found: %s\n", name)
				os.Exit(1)
			}

			if minSize >= 0 {
				nodepool.MinSize = minSize
			}
			if maxSize > 0 {
				nodepool.MaxSize = maxSize
			}
			if state != "" {
				nodepool.State = models.NodepoolState(state)
			}

			if err := np.root.nodepoolRepo.Update(ctx, nodepool); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully updated nodepool %s (%s)\n", nodepool.ID, nodepool.Name)
		},
	}

	cmd.Flags().IntVarP(&minSize, "min", "m", -1, "Minimum number of nodes")
	cmd.Flags().IntVarP(&maxSize, "max", "M", -1, "Maximum number of nodes")
	cmd.Flags().StringVar(&state, "state", "", "State (active, disabled, deleting)")

	return cmd
}

func newNodePoolDeleteCommand(np *NodePoolCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete a node pool",
		Long:  "Delete an existing node pool",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			name := args[0]

			nodepool, err := np.root.nodepoolRepo.GetByName(ctx, name)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if nodepool == nil {
				cmd.Printf("Error: nodepool not found: %s\n", name)
				os.Exit(1)
			}

			if err := np.root.nodepoolRepo.Delete(ctx, nodepool.ID); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully deleted nodepool %s (%s)\n", nodepool.ID, nodepool.Name)
		},
	}

	return cmd
}

func newNodePoolAssignCommand(np *NodePoolCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assign [pool] [machine]",
		Short: "Assign a machine to a pool",
		Long:  "Assign a machine to a node pool",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			poolName := args[0]
			machineID := args[1]

			nodepool, err := np.root.nodepoolRepo.GetByName(ctx, poolName)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if nodepool == nil {
				cmd.Printf("Error: nodepool not found: %s\n", poolName)
				os.Exit(1)
			}

			machine, err := np.root.machineRepo.GetByID(ctx, machineID)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if machine == nil {
				cmd.Printf("Error: machine not found: %s\n", machineID)
				os.Exit(1)
			}

			if err := np.root.machineRepo.AssignToNodepool(ctx, machine.ID, nodepool.ID); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully assigned machine %s to nodepool %s\n", machine.IPAddress, nodepool.Name)
		},
	}

	return cmd
}

func newNodePoolUnassignCommand(np *NodePoolCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unassign [pool] [machine]",
		Short: "Unassign a machine from a pool",
		Long:  "Unassign a machine from a node pool",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			machineID := args[1]

			machine, err := np.root.machineRepo.GetByID(ctx, machineID)
			if err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			if machine == nil {
				cmd.Printf("Error: machine not found: %s\n", machineID)
				os.Exit(1)
			}

			if err := np.root.machineRepo.UnassignFromNodepool(ctx, machine.ID); err != nil {
				cmd.Printf("Error: %v\n", err)
				os.Exit(1)
			}

			cmd.Printf("Successfully unassigned machine %s from nodepool\n", machine.IPAddress)
		},
	}

	return cmd
}

type DiscoveryCommand struct {
	root *RootCommand
}

func newDiscoveryCommand(root *RootCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover machines on the network",
		Long:  "Commands for discovering machines on the network via SSH",
	}

	d := &DiscoveryCommand{root: root}

	cmd.AddCommand(
		newDiscoveryScanCommand(d),
		newDiscoveryStatusCommand(d),
	)

	return cmd
}

func newDiscoveryScanCommand(d *DiscoveryCommand) *cobra.Command {
	var timeout int
	var port int
	var sshUser string
	var keyPath string
	var output string

	cmd := &cobra.Command{
		Use:   "scan [range]",
		Short: "Scan a network range for machines",
		Long: `Scan a network range (e.g., 192.168.1.0/24) for accessible machines
via SSH and add them to the discovery queue.`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			rangeArg := args[0]
			cmd.Printf("Scanning network range: %s (timeout: %ds, port: %d, user: %s)\n",
				rangeArg, timeout, port, sshUser)
			cmd.Println("Note: Network discovery requires the discovery service to be running.")
			cmd.Println("Results will be stored in the discovery_results table.")
			_ = output
		},
	}

	cmd.Flags().IntVarP(&timeout, "timeout", "t", 5, "Timeout in seconds per host")
	cmd.Flags().IntVarP(&port, "port", "p", 22, "SSH port")
	cmd.Flags().StringVarP(&sshUser, "user", "u", "root", "SSH username")
	cmd.Flags().StringVarP(&keyPath, "key", "k", "", "Path to SSH private key")
	cmd.Flags().StringVarP(&output, "output", "o", "list", "Output format (list, json)")

	return cmd
}

func newDiscoveryStatusCommand(d *DiscoveryCommand) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show discovery status",
		Long:  "Show the current status of the discovery process",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("Discovery status:")
			cmd.Println("Note: Run 'ksa discover scan' to discover machines on the network.")
			cmd.Println("Note: The discovery service must be running to perform scans.")
		},
	}

	return cmd
}

// Helper functions

func parseLabels(s string) models.JSONMap {
	labels := make(models.JSONMap)
	if s == "" {
		return labels
	}
	pairs := strings.Split(s, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			labels[kv[0]] = kv[1]
		}
	}
	return labels
}

func formatLabels(labels models.JSONMap) string {
	if labels == nil {
		return ""
	}
	var pairs []string
	for k, v := range labels {
		pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(pairs, ",")
}

func parseMachineFile(path, format string) ([]*models.Machine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if format == "auto" {
		if strings.HasSuffix(path, ".csv") {
			format = "csv"
		} else if strings.HasSuffix(path, ".json") {
			format = "json"
		} else {
			format = "yaml"
		}
	}

	switch format {
	case "yaml", "toml":
		var machines []*models.Machine
		if err := toml.Unmarshal(data, &machines); err != nil {
			return nil, fmt.Errorf("failed to parse YAML/TOML: %w", err)
		}
		return machines, nil
	case "json":
		var machines []*models.Machine
		if err := json.Unmarshal(data, &machines); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}
		return machines, nil
	case "csv":
		return parseMachinesCSV(data)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

func parseMachinesCSV(data []byte) ([]*models.Machine, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSV: %w", err)
	}

	var machines []*models.Machine
	for i, record := range records[1:] { // Skip header
		if len(record) < 3 {
			continue
		}

		machine := &models.Machine{
			Name:      record[0],
			IPAddress: record[1],
			SSHUser:   record[2],
			State:     models.MachineStateAvailable,
		}

		if len(record) > 3 {
			machine.SSHKeyPath = record[3]
		}
		if len(record) > 4 {
			port, _ := strconv.Atoi(record[4])
			machine.SSHPort = port
		}

		machines = append(machines, machine)
		_ = i
	}

	return machines, nil
}
