package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Group and resource coordinates of the two APIs the runner uses.
var (
	gvrClaims    = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims"}
	gvrSandboxes = schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}
	gvrPools     = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools"}
	gvrTemplates = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates"}
)

const (
	// managedBy labels everything this runner creates, so a leftover object can
	// be traced to the run that made it.
	managedBy    = "app.kubernetes.io/managed-by"
	managedValue = "session-lab"
	// labelClass records the workload class on a claim, so the pool's demand
	// signal can leave bulk work out of the capacity calculation.
	labelClass = "session-lab.io/class"
	// cdpPath is the in-pod bridge's command endpoint (apps/browser-sandbox).
	cdpPath = "/cdp"
)

// Kube is the adapter that talks to a real cluster.
type Kube struct {
	dyn    dynamic.Interface
	core   kubernetes.Interface
	client *http.Client

	// mu guards the claim cache below. The cache is filled by one watch so the
	// autoscaler's tick costs no API request.
	mu            sync.Mutex
	claims        map[string]claimObservation
	claimsReady   bool
	claimsErr     error
	claimsStarted bool
}

// setClaimCache replaces the cached claims and marks the cache warm.
func (k *Kube) setClaimCache(cache map[string]claimObservation) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.claims = cache
	k.claimsReady = true
}

// setClaimCacheError records why the watch is not running, so sandbox.ClaimStats can
// fall back to a direct read instead of reporting zero demand.
func (k *Kube) setClaimCacheError(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.claimsErr = err
	if err != nil {
		k.claimsReady = false
	}
}

// applyClaimCacheEvent folds one watch event into the cache.
func (k *Kube) applyClaimCacheEvent(event watch.Event) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.claims == nil {
		k.claims = map[string]claimObservation{}
	}
	applyClaimEvent(k.claims, event)
}

// ensureClaimCache starts the watch the first time counts are asked for.
func (k *Kube) ensureClaimCache(namespace string) {
	k.mu.Lock()
	if k.claimsStarted {
		k.mu.Unlock()
		return
	}
	k.claimsStarted = true
	k.mu.Unlock()
	go k.claimCacheLoop(namespace)
}

// NewKube builds an adapter from a kubeconfig path (empty uses the in-cluster
// config, then ~/.kube/config).
func NewKube(kubeconfig, contextName string) (*Kube, error) {
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)
	cfg, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	// The run creates and deletes one object per session; client-side throttling
	// is the wrong place to cap that, so the limit is raised and the platform
	// decides what it will accept.
	cfg.QPS = -1.0
	cfg.Burst = 400

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return &Kube{dyn: dyn, core: core, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

// Acquire creates the claim that will adopt (or cold-start) a sandbox.
func (k *Kube) Acquire(ctx context.Context, namespace, pool, name string, class model.Class) (*sandbox.Instance, error) {
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxClaim",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]any{managedBy: managedValue},
		},
		"spec": map[string]any{
			"warmPoolRef": map[string]any{"name": pool},
		},
	}}
	created, err := k.dyn.Resource(gvrClaims).Namespace(namespace).Create(ctx, claim, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create claim %s/%s: %w", namespace, name, err)
	}
	return &sandbox.Instance{
		ID:        created.GetName(),
		Kind:      sandbox.KindClaim,
		Namespace: namespace,
		Claim:     created.GetName(),
		CreatedNS: created.GetCreationTimestamp().UnixNano(),
	}, nil
}

// WaitReady blocks until the claim reports Ready, then fills in the pod
// identity. It watches that one claim: a Get every second per waiting session is
// what made a 150-session run hammer the API server. Adoption is visible in the
// timestamps: a sandbox created before the claim came from the pool, a sandbox
// created after it was a cold start.
func (k *Kube) WaitReady(ctx context.Context, instance *sandbox.Instance) error {
	return k.waitClaimReady(ctx, instance)
}

// fillFromSandbox reads the sandbox the claim adopted: creation order tells warm
// from cold, and the status carries the node, the pod IPs and the label selector
// that identifies the pod the sampler needs.
func (k *Kube) fillFromSandbox(ctx context.Context, instance *sandbox.Instance) (bool, error) {
	sandbox, err := k.dyn.Resource(gvrSandboxes).Namespace(instance.Namespace).Get(ctx, instance.Sandbox, metav1.GetOptions{})
	if err != nil {
		return false, nil
	}
	instance.Warm = sandbox.GetCreationTimestamp().UnixNano() < instance.CreatedNS
	if ips, ok, _ := unstructured.NestedStringSlice(sandbox.Object, "status", "podIPs"); ok && len(ips) > 0 {
		instance.PodIP = ips[0]
	}
	if node, ok, _ := unstructured.NestedString(sandbox.Object, "status", "nodeName"); ok {
		instance.Node = node
	}

	// The pod itself carries the UID the cgroup reader joins on. This cluster
	// labels the pod with status.selector and does not set a pod-name
	// annotation, so the selector is the reliable route.
	if selector, ok, _ := unstructured.NestedString(sandbox.Object, "status", "selector"); ok && selector != "" {
		pods, err := k.core.CoreV1().Pods(instance.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 1})
		if err == nil {
			for _, pod := range pods.Items {
				instance.PodUID = string(pod.UID)
				if instance.Node == "" {
					instance.Node = pod.Spec.NodeName
				}
				if instance.PodIP == "" && pod.Status.PodIP != "" {
					instance.PodIP = pod.Status.PodIP
				}
				break
			}
		}
	}
	// Older manifests annotate the pod name instead; keep that path as a
	// fallback rather than depending on either one.
	if instance.PodUID == "" {
		if podName, ok, _ := unstructured.NestedString(sandbox.Object, "metadata", "annotations", "agents.x-k8s.io/pod-name"); ok && podName != "" {
			if pod, err := k.core.CoreV1().Pods(instance.Namespace).Get(ctx, podName, metav1.GetOptions{}); err == nil {
				instance.PodUID = string(pod.UID)
				if instance.Node == "" {
					instance.Node = pod.Spec.NodeName
				}
				if instance.PodIP == "" && pod.Status.PodIP != "" {
					instance.PodIP = pod.Status.PodIP
				}
			}
		}
	}
	return instance.PodIP != "" || instance.PodUID != "", nil
}

// claimFailed reports a claim whose Ready condition carries a failure reason, so
// the autoscaler does not keep buying replicas for a claim that cannot be served.
func claimFailed(claim *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(claim.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok || condition["type"] != "Ready" || condition["status"] != "False" {
			continue
		}
		switch condition["reason"] {
		case "PodFailed", "Failed", "TemplateNotFound", "WarmPoolNotFound":
			return true
		}
	}
	return false
}

// claimState reports whether the claim is Ready and which sandbox it holds.
func claimState(claim *unstructured.Unstructured) (bool, string) {
	conditions, _, _ := unstructured.NestedSlice(claim.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] == "Ready" && condition["status"] == "True" {
			sandbox, _, _ := unstructured.NestedString(claim.Object, "status", "sandbox", "name")
			if sandbox == "" {
				sandbox, _, _ = unstructured.NestedString(claim.Object, "status", "sandbox", "metadata", "name")
			}
			return true, sandbox
		}
	}
	return false, ""
}

// Reset navigates the sandbox's browser to a blank page through the in-pod
// bridge, which is the same transport the console uses for CDP commands.
func (k *Kube) Reset(ctx context.Context, instance *sandbox.Instance) error {
	if instance.PodIP == "" {
		return fmt.Errorf("reset %s: no pod IP", instance.ID)
	}
	body, err := json.Marshal(map[string]any{
		"method": "Page.navigate",
		"params": map[string]any{"url": "about:blank"},
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s:9222%s", instance.PodIP, cdpPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sandbox-Port", "9222")
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("reset %s: %w", instance.ID, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reset %s: %s: %s", instance.ID, resp.Status, string(payload))
	}
	instance.Resets++
	return nil
}

// Suspend terminates the backing pod while keeping the sandbox and its volumes.
func (k *Kube) Suspend(ctx context.Context, instance *sandbox.Instance) error {
	return k.setOperatingMode(ctx, instance, "Suspended")
}

// Resume brings a suspended sandbox back by recreating its pod.
func (k *Kube) Resume(ctx context.Context, instance *sandbox.Instance) error {
	return k.setOperatingMode(ctx, instance, "Running")
}

func (k *Kube) setOperatingMode(ctx context.Context, instance *sandbox.Instance, mode string) error {
	if instance.Sandbox == "" {
		return fmt.Errorf("operating mode %s: %s has no sandbox", mode, instance.ID)
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"operatingMode": mode}})
	if err != nil {
		return err
	}
	if _, err := k.dyn.Resource(gvrSandboxes).Namespace(instance.Namespace).
		Patch(ctx, instance.Sandbox, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("set operating mode %s on %s: %w", mode, instance.ID, err)
	}
	if mode == "Suspended" {
		instance.Suspends++
		instance.Suspended = true
	} else {
		instance.Resumes++
		instance.Suspended = false
	}
	return nil
}

// Release deletes the claim, which takes the sandbox and its pod with it.
func (k *Kube) Release(ctx context.Context, instance *sandbox.Instance) error {
	err := k.dyn.Resource(gvrClaims).Namespace(instance.Namespace).
		Delete(ctx, instance.Claim, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("delete claim %s/%s: %w", instance.Namespace, instance.Claim, err)
	}
	instance.DeletedNS = time.Now().UnixNano()
	return nil
}

// sandbox.ClaimStats counts claims by readiness for one pool. The counts come from the
// watch-backed cache, so a five-second autoscaler tick costs no API request; if
// the watch is down it falls back to a direct list rather than reporting zero
// demand.
func (k *Kube) ClaimStats(ctx context.Context, namespace, pool string) (sandbox.ClaimStats, error) {
	k.ensureClaimCache(namespace)
	k.mu.Lock()
	cache, warm, watchErr := k.claims, k.claimsReady, k.claimsErr
	k.mu.Unlock()
	if warm && watchErr == nil {
		return countObservations(cache, pool), nil
	}

	claims, err := k.dyn.Resource(gvrClaims).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return sandbox.ClaimStats{}, fmt.Errorf("list claims %s: %w", namespace, err)
	}
	var stats sandbox.ClaimStats
	for i := range claims.Items {
		name, obs := observeClaim(&claims.Items[i])
		_ = name
		if pool != "" && obs.pool != "" && obs.pool != pool {
			continue
		}
		switch {
		case obs.bulk:
			stats.Bulk++
		case obs.ready:
			stats.Ready++
		case obs.failed:
			stats.Failed++
		default:
			stats.Pending++
		}
	}
	return stats, nil
}

// Pool reports the warm pool's replica counts and template.
func (k *Kube) Pool(ctx context.Context, namespace, name string) (sandbox.PoolState, error) {
	pool, err := k.dyn.Resource(gvrPools).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return sandbox.PoolState{}, fmt.Errorf("get pool %s/%s: %w", namespace, name, err)
	}
	state := sandbox.PoolState{Name: name}
	if replicas, ok, _ := unstructured.NestedInt64(pool.Object, "spec", "replicas"); ok {
		state.Desired = int32(replicas)
	}
	if ready, ok, _ := unstructured.NestedInt64(pool.Object, "status", "readyReplicas"); ok {
		state.Ready = int32(ready)
	}
	if ready, ok, _ := unstructured.NestedInt64(pool.Object, "status", "replicas"); ok && state.Ready == 0 {
		state.Ready = int32(ready)
	}
	if template, ok, _ := unstructured.NestedString(pool.Object, "spec", "sandboxTemplateRef", "name"); ok {
		state.Template = template
	}
	if value, ok, _ := unstructured.NestedString(pool.Object, "metadata", "annotations", sandbox.AnnotationAutoscalerPaused); ok && value == "disabled" {
		state.Paused = true
	}
	return state, nil
}

// SetPoolReplicas writes the pool's desired count.
func (k *Kube) SetPoolReplicas(ctx context.Context, namespace, name string, replicas int32) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"replicas": replicas}})
	if err != nil {
		return err
	}
	if _, err := k.dyn.Resource(gvrPools).Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch pool %s/%s replicas=%d: %w", namespace, name, replicas, err)
	}
	return nil
}

// Template reads the requests and limits the template applies to a sandbox pod,
// which is the context CPU throttling needs to mean anything.
func (k *Kube) Template(ctx context.Context, namespace, name string) (sandbox.TemplateLimits, error) {
	template, err := k.dyn.Resource(gvrTemplates).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return sandbox.TemplateLimits{}, fmt.Errorf("get template %s/%s: %w", namespace, name, err)
	}
	containers, found, _ := unstructured.NestedSlice(template.Object, "spec", "podTemplate", "spec", "containers")
	if !found || len(containers) == 0 {
		return sandbox.TemplateLimits{}, fmt.Errorf("template %s has no containers", name)
	}
	limits := sandbox.TemplateLimits{}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(container, "name")
		resources, _, _ := unstructured.NestedMap(container, "resources")
		if resources == nil {
			continue
		}
		limits.Container = name
		limits.CPURequestMilli = quantityMilli(resources, "requests", "cpu")
		limits.CPULimitMilli = quantityMilli(resources, "limits", "cpu")
		limits.MemRequestBytes = quantityBytes(resources, "requests", "memory")
		limits.MemLimitBytes = quantityBytes(resources, "limits", "memory")
		break
	}
	return limits, nil
}

// quantityMilli reads resources.<kind>.cpu as milli-cores.
func quantityMilli(resources map[string]any, kind, key string) int64 {
	raw, ok := resources[kind]
	if !ok {
		return 0
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return 0
	}
	text, ok := values[key].(string)
	if !ok {
		return 0
	}
	quantity, err := resource.ParseQuantity(text)
	if err != nil {
		return 0
	}
	return quantity.MilliValue()
}

// quantityBytes reads resources.<kind>.<key> as bytes.
func quantityBytes(resources map[string]any, kind, key string) int64 {
	raw, ok := resources[kind]
	if !ok {
		return 0
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return 0
	}
	text, ok := values[key].(string)
	if !ok {
		return 0
	}
	quantity, err := resource.ParseQuantity(text)
	if err != nil {
		return 0
	}
	return quantity.Value()
}

// SetPoolPaused annotates the pool so an autoscaler leaves it alone, or removes
// the annotation. An experiment that owns the replica count uses this instead of
// racing the scaler.
func (k *Kube) SetPoolPaused(ctx context.Context, namespace, name string, paused bool) error {
	value := ""
	if paused {
		value = "disabled"
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{sandbox.AnnotationAutoscalerPaused: value}},
	})
	if err != nil {
		return err
	}
	if _, err := k.dyn.Resource(gvrPools).Namespace(namespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("set pool %s/%s paused=%v: %w", namespace, name, paused, err)
	}
	return nil
}

// Node reads the allocatable budget of the node that holds the sandboxes.
func (k *Kube) Node(ctx context.Context, namespace string) (sandbox.NodeCapacity, error) {
	nodeName := ""
	pods, err := k.core.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 50})
	if err == nil {
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != "" {
				nodeName = pod.Spec.NodeName
				break
			}
		}
	}
	nodes, err := k.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return sandbox.NodeCapacity{}, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return sandbox.NodeCapacity{}, fmt.Errorf("no nodes")
	}
	node := nodes.Items[0]
	for i := range nodes.Items {
		if nodes.Items[i].Name == nodeName {
			node = nodes.Items[i]
			break
		}
	}
	allocatable := node.Status.Allocatable
	return sandbox.NodeCapacity{
		Name:     node.Name,
		CPUMilli: allocatable.Cpu().MilliValue(),
		MemBytes: allocatable.Memory().Value(),
		Pods:     allocatable.Pods().Value(),
	}, nil
}

// Boundaries reports refusals and degradations from the event stream.
func (k *Kube) Boundaries(ctx context.Context, namespace string, since time.Time) ([]sandbox.Boundary, error) {
	events, err := k.core.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]sandbox.Boundary, 0, 8)
	for _, event := range events.Items {
		at := event.LastTimestamp.Time
		if at.IsZero() {
			at = event.EventTime.Time
		}
		if at.Before(since) {
			continue
		}
		switch event.Reason {
		case "OOMKilling", "Evicted", "FailedScheduling", "FailedCreate", "FailedAttachVolume":
			out = append(out, sandbox.Boundary{
				At:      at,
				Kind:    event.Reason,
				Object:  event.InvolvedObject.Kind + "/" + event.InvolvedObject.Name,
				Message: event.Message,
			})
		}
	}
	return out, nil
}

var _ sandbox.Adapter = (*Kube)(nil)

// NodePathForCgroup returns the cgroup directory root for pod scopes on this
// host, or an empty string when it is not a cgroup v2 system.
func NodePathForCgroup() string {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return ""
	}
	return filepath.Join("/sys/fs/cgroup")
}

// PodCgroupDir finds the cgroup directory for a pod UID under root.
//
// systemd's cgroup driver spells the pod UID into the slice name with
// underscores, not dashes (kubepods-burstable-pod0b845006_2d0c_4338_....slice),
// and the container scopes live underneath it. The slice is the right level to
// read: it aggregates every container in the pod, which is what a session costs.
func PodCgroupDir(root, podUID string) (string, error) {
	if podUID == "" {
		return "", fmt.Errorf("no pod UID")
	}
	forms := []string{podUID, strings.ReplaceAll(podUID, "-", "_")}
	for _, form := range forms {
		for _, pattern := range []string{
			filepath.Join(root, "kubepods.slice", "*", "*pod"+form+"*.slice"),
			filepath.Join(root, "kubepods.slice", "*", "*"+form+"*.slice"),
			filepath.Join(root, "kubepods.slice", "*", "*"+form+"*.scope"),
			filepath.Join(root, "kubepods.slice", "*"+form+"*.slice"),
		} {
			matches, err := filepath.Glob(pattern)
			if err != nil || len(matches) == 0 {
				continue
			}
			return matches[0], nil
		}
	}
	return "", fmt.Errorf("no cgroup for pod %s under %s", podUID, root)
}
