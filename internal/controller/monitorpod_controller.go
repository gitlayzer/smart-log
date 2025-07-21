/*
Copyright 2025 gitlayzer.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html/template"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"net/http"
	"reflect"
	"regexp"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
)

// MonitorPodReconciler reconciles a MonitorPod object
type MonitorPodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// 将 Manager 注入到 Reconciler 中
	Manager *MonitorManager
}

// MonitorManager 负责管理所有 MonitorPod 的后台监控任务
type MonitorManager struct {
	// 用于保护并发访问 monitors map
	mu sync.RWMutex
	// 存储每个监控任务的 cancel 函数，key 是 "namespace/name"
	monitors map[string]context.CancelFunc

	// K8S 客户端
	client.Client

	// 用于获取日志流的 clientset
	clientset *kubernetes.Clientset

	// 频率限制器
	rateLimiter *InMemoryRateLimiter
}

// RateLimiterInfo 存储了单个规则的告警频率信息
type RateLimiterInfo struct {
	Count      int
	LastSentAt time.Time
}

// InMemoryRateLimiter 是一个简单的内存频率限制器
type InMemoryRateLimiter struct {
	mu   sync.Mutex
	data map[string]RateLimiterInfo
}

// TemplateData 用于渲染告警模板
type TemplateData struct {
	PodName       string
	Namespace     string
	ContainerName string
	LogLine       string
	RuleName      string
	Timestamp     time.Time
}

// NewInMemoryRateLimiter 创建一个新的 InMemoryRateLimiter
func NewInMemoryRateLimiter() *InMemoryRateLimiter {
	return &InMemoryRateLimiter{
		data: make(map[string]RateLimiterInfo),
	}
}

// Allow 检查是否允许发送告警
func (rl *InMemoryRateLimiter) Allow(key string, limit int, period time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	info, exists := rl.data[key]

	// 检查周期是否已重置
	if exists && time.Since(info.LastSentAt) > period {
		// 如果超过了抑制周期，则删除旧记录，视为一次新请求
		delete(rl.data, key)
		exists = false
	}

	if !exists {
		// 第一次或已重置，允许并记录
		rl.data[key] = RateLimiterInfo{Count: 1, LastSentAt: time.Now()}
		return true
	}

	// 检查是否已达到上限
	if info.Count >= limit {
		// 已达到上限，不允许
		return false
	}

	// 未达到上限，允许，并增加计数
	info.Count++
	info.LastSentAt = time.Now() // 每次都更新时间戳
	rl.data[key] = info
	return true
}

// IsThrottled 检查是否应该抑制告警
func (rl *InMemoryRateLimiter) IsThrottled(key string, limit int, period time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	info, exists := rl.data[key]
	if !exists {
		return false // 第一次，不抑制
	}

	// 如果上次发送时间已经超过了抑制周期，重置计数器
	if time.Since(info.LastSentAt) > period {
		delete(rl.data, key)
		return false
	}

	// 检查是否达到次数上限
	return info.Count >= limit
}

// Record 记录一次成功的告警发送
func (rl *InMemoryRateLimiter) Record(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	info := rl.data[key]
	info.Count++
	info.LastSentAt = time.Now()
	rl.data[key] = info
}

// NewMonitorManager 创建一个新的 Manager
func NewMonitorManager(cli client.Client, cfg *rest.Config) (*MonitorManager, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &MonitorManager{
		monitors:    make(map[string]context.CancelFunc),
		Client:      cli,
		clientset:   clientset,
		rateLimiter: NewInMemoryRateLimiter(),
	}, nil
}

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertgroups,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MonitorPod object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *MonitorPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	log.Info("--- Received reconcile request for MonitorPod ---", "request", req.NamespacedName)

	// 1. 获取 MonitorPod 实例
	var monitorPod smartlogv1alpha1.MonitorPod
	if err := r.Get(ctx, req.NamespacedName, &monitorPod); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("MonitorPod resource not found. Stopping monitor.")
			r.Manager.StopMonitor(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get MonitorPod")
		return ctrl.Result{}, err
	}

	// 使用 defer 来确保无论函数如何退出，status 更新都会被尝试执行
	// 先复制一份原始状态，避免在函数中意外修改
	originalStatus := *monitorPod.Status.DeepCopy()
	defer func() {
		// 只有在 status 发生变化时才执行更新，避免不必要的 API 调用
		if !reflect.DeepEqual(originalStatus, monitorPod.Status) {
			log.Info("Updating MonitorPod status")
			if err := r.Status().Update(ctx, &monitorPod); err != nil {
				log.Error(err, "Failed to update MonitorPod status")
			}
		}
	}()

	// 2. 【新增】计算并更新可立即获得的状态
	var podList corev1.PodList
	selector, err := metav1.LabelSelectorAsSelector(&monitorPod.Spec.Selector)
	if err != nil {
		log.Error(err, "Invalid label selector in MonitorPod spec")
		meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSelector",
			Message: err.Error(),
		})
		// 无需继续，defer 会处理 status 更新
		return ctrl.Result{}, nil
	}

	// 在 MonitorPod 所在的命名空间内查找匹配的 Pod
	if err := r.List(ctx, &podList, client.InNamespace(monitorPod.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "Failed to list pods for status update")
		meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ListPodsFailed",
			Message: err.Error(),
		})
		return ctrl.Result{}, err // 返回 err 以便重试
	}

	log.Info("Found matching pods", "count", len(podList.Items))

	// <<< 将日志循环移动到这里 >>>
	podNames := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		log.Info("Matched pod details", "podName", pod.Name, "podNamespace", pod.Namespace)
		podNames = append(podNames, pod.Name)
	}

	// 更新状态字段
	monitorPod.Status.MonitoredPodsCount = len(podList.Items)

	// 设置 Ready 状态
	meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "SetupComplete",
		Message: "MonitorPod is configured correctly and monitoring pods.",
	})

	// 3. 委托后台任务给 Manager
	log.Info("Delegating to MonitorManager", "monitoredPodsCount", monitorPod.Status.MonitoredPodsCount)
	r.Manager.StartOrUpdateMonitor(ctx, &monitorPod)

	return ctrl.Result{}, nil
}

// StartOrUpdateMonitor 启动或更新指定命名空间下的指定名称的监控任务
func (m *MonitorManager) StartOrUpdateMonitor(ctx context.Context, mp *smartlogv1alpha1.MonitorPod) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := types.NamespacedName{Name: mp.Name, Namespace: mp.Namespace}.String()

	if cancel, exists := m.monitors[key]; exists {
		cancel()
	}

	// 从 Reconcile 传入的 ctx 创建子 context，这样 logger 等值就能被继承
	monitorCtx, cancel := context.WithCancel(ctx) // <<< 修改这里！

	m.monitors[key] = cancel
	go m.runMonitorLoop(monitorCtx, mp)
}

// StopMonitor 停止指定命名空间下的指定名称的监控任务
func (m *MonitorManager) StopMonitor(namespacedName types.NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := namespacedName.String()
	if cancel, exists := m.monitors[key]; exists {
		cancel()
		delete(m.monitors, key)
	}
}

// runMonitorLoop 是为单个 MonitorPod 资源运行的主循环
func (m *MonitorManager) runMonitorLoop(ctx context.Context, mp *smartlogv1alpha1.MonitorPod) {
	log := logf.FromContext(ctx).WithValues("MonitorPod", mp.Name)
	log.Info("Starting monitor loop")
	defer log.Info("Stopping monitor loop")

	// 使用 Ticker 定期检查匹配的 Pods
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// 存储每个 Pod 的日志监听任务的 cancel 函数
	activePodMonitors := make(map[string]context.CancelFunc)
	var podMu sync.Mutex

	for {
		select {
		case <-ctx.Done(): // MonitorPod 被删除或更新，退出主循环
			podMu.Lock()
			for _, cancel := range activePodMonitors {
				cancel()
			}
			podMu.Unlock()
			return
		case <-ticker.C:
			// --- 查找和同步 Pods ---
			selector, err := metav1.LabelSelectorAsSelector(&mp.Spec.Selector)
			if err != nil {
				log.Error(err, "Invalid label selector")
				continue
			}

			var podList corev1.PodList
			if err := m.List(context.Background(), &podList, client.InNamespace(mp.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
				log.Error(err, "Failed to list pods")
				continue
			}

			podMu.Lock()

			currentPods := make(map[string]struct{})
			for _, pod := range podList.Items {
				if pod.Status.Phase == corev1.PodRunning {
					podKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}.String()
					currentPods[podKey] = struct{}{}
				}
			}

			// 步骤 2: 清理不再存在的 Pod 的监控
			for podKey, cancel := range activePodMonitors {
				if _, exists := currentPods[podKey]; !exists {
					log.Info("Stopping monitor for pod that no longer exists or matches", "podKey", podKey)
					cancel()                          // 停止 goroutine
					delete(activePodMonitors, podKey) // 从 map 中移除
				}
			}

			// (此处省略了复杂的 Pod 同步逻辑，简化为只为新 Pod 启动)
			for _, pod := range podList.Items {
				if pod.Status.Phase != corev1.PodRunning {
					continue
				}
				podKey := types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}.String()
				if _, exists := activePodMonitors[podKey]; !exists {
					log.Info("Found new pod to monitor", "pod", pod.Name)
					podCtx, podCancel := context.WithCancel(ctx)
					activePodMonitors[podKey] = podCancel

					go m.streamPodLogs(podCtx, mp, pod.DeepCopy())
				}
			}
			podMu.Unlock()
		}
	}
}

// streamPodLogs 监听单个 Pod 的日志流
func (m *MonitorManager) streamPodLogs(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, pod *corev1.Pod) {
	log := logf.FromContext(ctx).WithValues("Pod", pod.Name)
	log.Info("Starting log stream")
	defer log.Info("Closing log stream")

	// (此处省略了处理多容器的逻辑，简化为第一个容器)
	containerName := pod.Spec.Containers[0].Name

	req := m.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: containerName,
		Follow:    true,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		log.Error(err, "Failed to open log stream")
		return
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		logLine := scanner.Text()

		for _, rule := range mp.Spec.Rules {
			matched, _ := regexp.MatchString(rule.Regex, logLine)
			if matched {
				log.Info("Log line matched rule", "rule", rule.Name, "log", logLine)

				// 在独立的 goroutine 中发送告警，避免阻塞日志读取
				go m.dispatchAlert(ctx, mp, pod, logLine, rule, containerName) // <<< 修改这里！
				break                                                          // 一行日志只匹配第一个规则
			}
		}
	}
}

// dispatchAlert 负责执行最终的告警发送
// dispatchAlert 负责执行最终的告警发送和状态更新 (最终完整版 - 包含模板优先级和频率限制)
func (m *MonitorManager) dispatchAlert(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, pod *corev1.Pod, logLine string, rule smartlogv1alpha1.LogRule, containerName string) {
	// 为本次派发任务创建一个基础的、带上下文信息的 logger
	baseLog := logf.FromContext(ctx).WithValues("MonitorPod", mp.Name, "Pod", pod.Name, "Rule", rule.Name)

	// --- 步骤 1: 频率限制检查与记录 (原子操作) ---
	if mp.Spec.RateLimit != nil && mp.Spec.RateLimit.Limit > 0 {
		rateLimitKey := fmt.Sprintf("%s/%s/%s", mp.Namespace, mp.Name, rule.Name)
		period, err := time.ParseDuration(mp.Spec.RateLimit.Period)
		if err != nil {
			baseLog.Error(err, "无效的频率限制周期格式，使用默认值 5m")
			period = 5 * time.Minute
		}

		// 调用 Allow()，它会原子地完成检查和记录
		if !m.rateLimiter.Allow(rateLimitKey, mp.Spec.RateLimit.Limit, period) {
			baseLog.Info("告警已被频率限制器抑制")
			return
		}
		baseLog.Info("频率限制检查通过，准备发送告警")
	}

	// --- 步骤 2: 获取所有有效的告警渠道配置 ---
	alerts, err := m.getAlertsFromTarget(ctx, &mp.Spec.AlertTarget, mp.Namespace)
	if err != nil {
		baseLog.Error(err, "获取告警渠道配置失败")
		return
	}
	if len(alerts) == 0 {
		baseLog.Info("未找到可用的告警渠道，跳过通知")
		return
	}

	// --- 步骤 3: 准备模板数据 (只需准备一次) ---
	templateData := TemplateData{
		PodName:       pod.Name,
		Namespace:     pod.Namespace,
		ContainerName: containerName,
		LogLine:       logLine,
		RuleName:      rule.Name,
		Timestamp:     time.Now(),
	}

	// --- 步骤 4: 并发发送告警到所有渠道 ---
	var wg sync.WaitGroup
	successCh := make(chan bool, len(alerts))

	for _, alert := range alerts {
		wg.Add(1)
		go func(alertConf smartlogv1alpha1.Alert) {
			defer wg.Done()
			alertLog := baseLog.WithValues("alert", alertConf.Name)

			// --- 【核心修改】模板优先级选择逻辑 ---
			templateStr := mp.Spec.AlertTemplate // 默认使用 MonitorPod 的模板
			if templateStr == "" && alertConf.Spec.Webhook != nil && alertConf.Spec.Webhook.BodyTemplate != "" {
				// 如果 MonitorPod 模板为空，则回退到 Alert 的模板
				alertLog.Info("MonitorPod 模板为空，回退到 Alert 的 bodyTemplate")
				templateStr = alertConf.Spec.Webhook.BodyTemplate
			}

			// --- 渲染最终选择的模板 ---
			messageBody, err := m.renderAlertTemplate(templateStr, templateData)
			if err != nil {
				alertLog.Error(err, "渲染告警模板失败")
				return
			}

			// 安全检查，确保 webhook 配置存在
			if alertConf.Spec.Webhook == nil {
				alertLog.Error(fmt.Errorf("webhook spec is nil for alert"), "Webhook 配置缺失")
				return
			}

			// 从 Secret 中获取 Webhook URL
			var secret corev1.Secret
			secretKey := types.NamespacedName{Name: alertConf.Spec.Webhook.URLSecretRef.Name, Namespace: alertConf.Namespace}
			if err := m.Get(ctx, secretKey, &secret); err != nil {
				alertLog.Error(err, "获取 Webhook 的 Secret 失败", "secretName", secretKey.Name)
				return
			}
			webhookURLBytes, ok := secret.Data[alertConf.Spec.Webhook.URLSecretRef.Key]
			if !ok {
				alertLog.Error(fmt.Errorf("key not found in secret"), "在 Secret 中未找到指定的 key", "secretKey", alertConf.Spec.Webhook.URLSecretRef.Key)
				return
			}
			webhookURL := string(webhookURLBytes)

			// 为 HTTP 请求创建独立的、带超时的上下文
			reqCtx, cancelReq := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancelReq()

			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewBuffer(messageBody))
			if err != nil {
				alertLog.Error(err, "创建 HTTP 请求失败")
				return
			}

			// 添加自定义 Headers
			req.Header.Set("Content-Type", "application/json")
			for _, header := range alertConf.Spec.Webhook.Headers {
				req.Header.Set(header.Name, header.Value)
			}

			// 发送请求
			httpClient := &http.Client{}
			resp, err := httpClient.Do(req)
			if err != nil {
				alertLog.Error(err, "发送 Webhook 通知失败")
				return
			}
			defer resp.Body.Close()

			// 检查响应状态码
			if resp.StatusCode >= 400 {
				err = fmt.Errorf("Webhook 服务端返回错误状态码: %s", resp.Status)
				alertLog.Error(err, "Webhook 发送失败")
				return
			}

			alertLog.Info("成功发送通知")
			successCh <- true
		}(alert)
	}

	wg.Wait()
	close(successCh)

	// --- 步骤 5: 根据发送结果更新状态 ---
	if len(successCh) > 0 {
		namespacedName := types.NamespacedName{Name: mp.Name, Namespace: mp.Namespace}
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			updateCtx := context.Background()
			var latestMonitorPod smartlogv1alpha1.MonitorPod
			if errGet := m.Get(updateCtx, namespacedName, &latestMonitorPod); errGet != nil {
				return errGet
			}

			latestMonitorPod.Status.AlertsSentCount++
			now := metav1.Now()
			latestMonitorPod.Status.LastTriggeredTime = &now

			return m.Status().Update(updateCtx, &latestMonitorPod)
		})

		if err != nil {
			baseLog.Error(err, "发送告警后更新 MonitorPod 状态失败")
		} else {
			baseLog.Info("成功更新 MonitorPod 的告警状态")
		}
	}
}

// getAlertsFromTarget 根据 AlertTarget 获取所有有效的 Alert 资源
func (m *MonitorManager) getAlertsFromTarget(ctx context.Context, target *smartlogv1alpha1.AlertTarget, namespace string) ([]smartlogv1alpha1.Alert, error) {
	readyAlerts := make([]smartlogv1alpha1.Alert, 0)

	if target.Kind == "Alert" {
		var alert smartlogv1alpha1.Alert
		key := types.NamespacedName{Name: target.Name, Namespace: namespace}
		if err := m.Get(ctx, key, &alert); err != nil {
			return nil, err
		}
		if alert.Status.Ready {
			readyAlerts = append(readyAlerts, alert)
		}
	} else if target.Kind == "AlertGroup" {
		var group smartlogv1alpha1.AlertGroup
		key := types.NamespacedName{Name: target.Name, Namespace: namespace}
		if err := m.Get(ctx, key, &group); err != nil {
			return nil, err
		}
		for _, alertName := range group.Spec.AlertNames {
			var alert smartlogv1alpha1.Alert
			alertKey := types.NamespacedName{Name: alertName, Namespace: namespace}
			if err := m.Get(ctx, alertKey, &alert); err != nil {
				// 组内某个成员获取失败，可以记录日志后跳过
				continue
			}
			if alert.Status.Ready {
				readyAlerts = append(readyAlerts, alert)
			}
		}
	} else {
		return nil, fmt.Errorf("unsupported alert target kind: %s", target.Kind)
	}

	return readyAlerts, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MonitorPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.MonitorPod{}).
		Named("monitorpod").
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findMonitorPodsForPod),
		).
		Complete(r)
}

// renderAlertTemplate 渲染告警消息模板
func (m *MonitorManager) renderAlertTemplate(templateStr string, data TemplateData) ([]byte, error) {
	if templateStr == "" {
		// 如果模板为空，提供一个默认的 JSON 模板
		templateStr = `{
			"pod": "{{.PodName}}",
			"namespace": "{{.Namespace}}",
			"container": "{{.ContainerName}}",
			"rule": "{{.RuleName}}",
			"log": "{{.LogLine}}",
			"timestamp": "{{.Timestamp.Format "2006-01-02T15:04:05Z07:00"}}"
		}`
	}

	tmpl, err := template.New("alert").Parse(templateStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse alert template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute alert template: %w", err)
	}

	return buf.Bytes(), nil
}

// findMonitorPodsForPod 是一个 MapFunc，用于找到所有可能匹配给定 Pod 的 MonitorPod。
func (r *MonitorPodReconciler) findMonitorPodsForPod(ctx context.Context, podObj client.Object) []reconcile.Request {
	pod, ok := podObj.(*corev1.Pod)
	if !ok {
		return []reconcile.Request{}
	}

	var monitorPods smartlogv1alpha1.MonitorPodList

	// 【修复】只在 Pod 所在的命名空间内查找 MonitorPod
	// 这样 List 操作就不会因为权限问题而失败
	if err := r.List(ctx, &monitorPods, client.InNamespace(pod.GetNamespace())); err != nil {
		// 如果 listing 失败，暂时返回空，避免卡死队列
		// 可以在这里加一行日志来观察错误
		// log.FromContext(ctx).Error(err, "failed to list monitorpods for pod watch")
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0)
	for _, mp := range monitorPods.Items {
		// 无需再检查 namespace，因为 List 的时候已经限定了

		selector, err := metav1.LabelSelectorAsSelector(&mp.Spec.Selector)
		if err != nil {
			continue
		}

		if selector.Matches(labels.Set(pod.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      mp.Name,
					Namespace: mp.Namespace,
				},
			})
		}
	}

	return requests
}
