/*
Copyright 2024 Kube Static Pool Autoscaler contributors
SPDX-License-Identifier: Apache-2.0
*/

package k8s

import (
	"context"
	"fmt"
	"time"

	"kube-static-pool-autoscaler/internal/config"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/sirupsen/logrus"
)

// Client wraps the Kubernetes client
type Client struct {
	clientset *kubernetes.Clientset
	logger    *logrus.Entry
}

// NewClient creates a new Kubernetes client
func NewClient(cfg *config.K8sConfig, logger *logrus.Entry) (*Client, error) {
	var restConfig *rest.Config
	var err error

	if cfg.Kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
		}
	} else {
		// Try in-cluster config first
		restConfig, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get kubernetes config: %w (consider setting kubeconfig path)", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &Client{
		clientset: clientset,
		logger:    logger.WithField("component", "k8s-client"),
	}, nil
}

// GetNodes returns all nodes with the given labels
func (c *Client) GetNodes(ctx context.Context, labelSelector string) ([]*corev1.Node, error) {
	var nodes *corev1.NodeList
	var err error

	if labelSelector != "" {
		nodes, err = c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
	} else {
		nodes, err = c.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	result := make([]*corev1.Node, len(nodes.Items))
	for i := range nodes.Items {
		result[i] = &nodes.Items[i]
	}

	return result, nil
}

// GetNode returns a single node by name
func (c *Client) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", name, err)
	}
	return node, nil
}

// Cordon marks a node as unschedulable
func (c *Client) Cordon(ctx context.Context, nodeName string) error {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Check if already cordoned
	if node.Spec.Unschedulable {
		c.logger.Debugf("Node %s is already cordoned", nodeName)
		return nil
	}

	// Add unschedulable taint
	node.Spec.Unschedulable = true

	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to cordon node: %w", err)
	}

	c.logger.Infof("Cordoned node %s", nodeName)
	return nil
}

// Uncordon marks a node as schedulable
func (c *Client) Uncordon(ctx context.Context, nodeName string) error {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	if !node.Spec.Unschedulable {
		c.logger.Debugf("Node %s is already schedulable", nodeName)
		return nil
	}

	node.Spec.Unschedulable = false

	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to uncordon node: %w", err)
	}

	c.logger.Infof("Uncordoned node %s", nodeName)
	return nil
}

// Drain drains a node
func (c *Client) Drain(ctx context.Context, nodeName string, options DrainOptions) error {
	c.logger.Infof("Draining node %s", nodeName)

	// First, cordon the node
	if err := c.Cordon(ctx, nodeName); err != nil {
		return err
	}

	// Get pods on the node
	pods, err := c.getPodsForNode(ctx, nodeName)
	if err != nil {
		return err
	}

	// Delete or evict pods
	for _, pod := range pods {
		if options.DeleteEmptyDir && pod.Spec.Volumes != nil {
			for _, vol := range pod.Spec.Volumes {
				if vol.EmptyDir != nil {
					// Delete the pod
					c.logger.Infof("Deleting pod %s/%s (has empty dir)", pod.Namespace, pod.Name)
					err := c.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
					if err != nil {
						c.logger.Warnf("Failed to delete pod %s/%s: %v", pod.Namespace, pod.Name, err)
					}
					continue
				}
			}
		}

		if options.Force {
			// Delete the pod
			c.logger.Infof("Deleting pod %s/%s (force)", pod.Namespace, pod.Name)
			err := c.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))})
			if err != nil {
				c.logger.Warnf("Failed to delete pod %s/%s: %v", pod.Namespace, pod.Name, err)
			}
		}
	}

	c.logger.Infof("Drain completed for node %s", nodeName)
	return nil
}

// DrainOptions holds options for draining a node
type DrainOptions struct {
	DeleteEmptyDir bool
	Force          bool
	Timeout        time.Duration
}

// getPodsForNode returns all pods running on a node
func (c *Client) getPodsForNode(ctx context.Context, nodeName string) ([]*corev1.Pod, error) {
	pods, err := c.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	var result []*corev1.Pod
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Spec.NodeName == nodeName {
			// Skip completed pods
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			// Skip daemon sets
			ownerRefs := pod.GetOwnerReferences()
			for _, ref := range ownerRefs {
				if ref.Kind == "DaemonSet" {
					continue
				}
			}
			result = append(result, pod)
		}
	}

	return result, nil
}

// CreateNode creates a new node object (for registration)
func (c *Client) CreateNode(ctx context.Context, node *corev1.Node) (*corev1.Node, error) {
	result, err := c.clientset.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}
	return result, nil
}

// UpdateNode updates a node object
func (c *Client) UpdateNode(ctx context.Context, node *corev1.Node) (*corev1.Node, error) {
	result, err := c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update node: %w", err)
	}
	return result, nil
}

// DeleteNode deletes a node
func (c *Client) DeleteNode(ctx context.Context, nodeName string) error {
	err := c.clientset.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete node: %w", err)
	}
	return nil
}

// GetNodeResources returns the allocatable resources of a node
func (c *Client) GetNodeResources(ctx context.Context, nodeName string) (cpu, memory int64, err error) {
	node, err := c.GetNode(ctx, nodeName)
	if err != nil {
		return 0, 0, err
	}

	cpuStr, ok := node.Status.Allocatable[corev1.ResourceCPU]
	if !ok {
		return 0, 0, fmt.Errorf("cpu not found in node status")
	}

	memoryStr, ok := node.Status.Allocatable[corev1.ResourceMemory]
	if !ok {
		return 0, 0, fmt.Errorf("memory not found in node status")
	}

	cpuQty := cpuStr.MilliValue()
	memoryQty := memoryStr.Value()

	return cpuQty, memoryQty, nil
}

// LabelNode adds a label to a node
func (c *Client) LabelNode(ctx context.Context, nodeName string, labels map[string]string) error {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	for k, v := range labels {
		node.Labels[k] = v
	}

	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to label node: %w", err)
	}

	return nil
}

// TaintNode adds a taint to a node
func (c *Client) TaintNode(ctx context.Context, nodeName string, taint corev1.Taint) error {
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Check if taint already exists
	for _, t := range node.Spec.Taints {
		if t.Key == taint.Key && t.Effect == taint.Effect {
			c.logger.Debugf("Taint %s:%s already exists on node %s", taint.Key, taint.Effect, nodeName)
			return nil
		}

		// Remove existing taint with same key and effect
		if t.Key == taint.Key {
			var newTaints []corev1.Taint
			for _, existingTaint := range node.Spec.Taints {
				if existingTaint.Key != taint.Key || existingTaint.Effect != taint.Effect {
					newTaints = append(newTaints, existingTaint)
				}
			}
			node.Spec.Taints = newTaints
		}
	}

	node.Spec.Taints = append(node.Spec.Taints, taint)

	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to taint node: %w", err)
	}

	return nil
}

// HealthCheck checks if the Kubernetes API is accessible
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("kubernetes API not accessible: %w", err)
	}
	return nil
}

// ptr returns a pointer to the value
func ptr[T any](v T) *T {
	return &v
}

// CreateNodeTemplate creates a node template from machine specs
func CreateNodeTemplate(machineName string, cpu int64, memory int64, gpu string, gpuCount int64, labels, taints map[string]string) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   machineName,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			Unschedulable: false,
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{},
			Capacity:    corev1.ResourceList{},
		},
	}

	// Set capacity
	node.Status.Capacity[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpu, resource.DecimalSI)
	node.Status.Capacity[corev1.ResourceMemory] = *resource.NewQuantity(memory, resource.BinarySI)

	if gpu != "" && gpuCount > 0 {
		node.Status.Capacity[corev1.ResourceName(fmt.Sprintf("nvidia.com/gpu"))] = *resource.NewQuantity(gpuCount, resource.DecimalSI)
	}

	// Set allocatable (same as capacity for new node)
	node.Status.Allocatable[corev1.ResourceCPU] = *resource.NewMilliQuantity(cpu, resource.DecimalSI)
	node.Status.Allocatable[corev1.ResourceMemory] = *resource.NewQuantity(memory, resource.BinarySI)

	if gpu != "" && gpuCount > 0 {
		node.Status.Allocatable[corev1.ResourceName(fmt.Sprintf("nvidia.com/gpu"))] = *resource.NewQuantity(gpuCount, resource.DecimalSI)
	}

	// Set node conditions
	node.Status.Conditions = []corev1.NodeCondition{
		{
			Type:               corev1.NodeReady,
			Status:             corev1.ConditionUnknown,
			Reason:             "KubeStaticPoolAutoscalerInitializing",
			Message:            "Node is being initialized by KSA",
			LastTransitionTime: metav1.Now(),
		},
	}

	// Add taints
	if len(taints) > 0 {
		var taintList []corev1.Taint
		for key, value := range taints {
			taintList = append(taintList, corev1.Taint{
				Key:    key,
				Value:  value,
				Effect: corev1.TaintEffectNoSchedule,
			})
		}
		node.Spec.Taints = taintList
	}

	klog.Infof("Created node template for %s", machineName)
	return node
}

// Initialize klog for k8s client-go
func init() {
	klog.InitFlags(nil)
}
