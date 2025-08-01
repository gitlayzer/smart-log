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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MonitorPodReconciler reconciles a MonitorPod object
type MonitorPodReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Manager *MonitorManager
}

// MonitorManager 负责管理所有 MonitorPod 的后台监控任务
type MonitorManager struct {
	mu       sync.RWMutex
	monitors map[string]context.CancelFunc
	client.Client
	clientset   *kubernetes.Clientset
	rateLimiter *InMemoryRateLimiter
	Scheme      *runtime.Scheme
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

// Allow 检查是否允许请求，如果允许则立即记录。这是一个原子操作。
func (rl *InMemoryRateLimiter) Allow(key string, limit int, period time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	info, exists := rl.data[key]
	if exists && time.Since(info.LastSentAt) > period {
		delete(rl.data, key)
		exists = false
	}
	if !exists {
		rl.data[key] = RateLimiterInfo{Count: 1, LastSentAt: time.Now()}
		return true
	}
	if info.Count >= limit {
		return false
	}
	info.Count++
	info.LastSentAt = time.Now()
	rl.data[key] = info
	return true
}

// NewMonitorManager 创建一个新的 Manager
func NewMonitorManager(cli client.Client, cfg *rest.Config, scheme *runtime.Scheme) (*MonitorManager, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &MonitorManager{
		monitors:    make(map[string]context.CancelFunc),
		Client:      cli,
		clientset:   clientset,
		rateLimiter: NewInMemoryRateLimiter(),
		Scheme:      scheme,
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
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertrecords,verbs=create

// Reconcile 函数处理 MonitorPod 的调谐
func (r *MonitorPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("--- Received reconcile request for MonitorPod ---", "request", req.NamespacedName)

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

	originalStatus := *monitorPod.Status.DeepCopy()
	defer func() {
		if !reflect.DeepEqual(originalStatus, monitorPod.Status) {
			log.Info("Updating MonitorPod status")
			if err := r.Status().Update(ctx, &monitorPod); err != nil {
				log.Error(err, "Failed to update MonitorPod status")
			}
		}
	}()

	var podList corev1.PodList
	selector, err := metav1.LabelSelectorAsSelector(&monitorPod.Spec.Selector)
	if err != nil {
		log.Error(err, "Invalid label selector in MonitorPod spec")
		meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InvalidSelector", Message: err.Error()})
		return ctrl.Result{}, nil
	}

	log.Info("Attempting to find pods with selector", "selector", selector.String())

	if err := r.List(ctx, &podList, client.InNamespace(monitorPod.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "Failed to list pods for status update")
		meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ListPodsFailed", Message: err.Error()})
		return ctrl.Result{}, err
	}

	log.Info("Found matching pods", "count", len(podList.Items))
	podNames := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		log.Info("Matched pod details", "podName", pod.Name, "podNamespace", pod.Namespace)
		podNames = append(podNames, pod.Name)
	}

	monitorPod.Status.MonitoredPodsCount = len(podList.Items)
	meta.SetStatusCondition(&monitorPod.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "SetupComplete", Message: "MonitorPod is configured correctly and monitoring pods."})
	log.Info("Delegating to MonitorManager", "monitoredPodsCount", monitorPod.Status.MonitoredPodsCount)
	r.Manager.StartOrUpdateMonitor(ctx, &monitorPod)

	return ctrl.Result{}, nil
}

// StartOrUpdateMonitor 函数启动或更新监控
func (m *MonitorManager) StartOrUpdateMonitor(ctx context.Context, mp *smartlogv1alpha1.MonitorPod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := types.NamespacedName{Name: mp.Name, Namespace: mp.Namespace}.String()
	if cancel, exists := m.monitors[key]; exists {
		cancel()
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	m.monitors[key] = cancel
	go m.runMonitorLoop(monitorCtx, mp)
}

// StopMonitor 函数停止监控
func (m *MonitorManager) StopMonitor(namespacedName types.NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := namespacedName.String()
	if cancel, exists := m.monitors[key]; exists {
		cancel()
		delete(m.monitors, key)
	}
}

// runMonitorLoop 函数运行监控循环
func (m *MonitorManager) runMonitorLoop(ctx context.Context, mp *smartlogv1alpha1.MonitorPod) {
	log := logf.FromContext(ctx).WithValues("MonitorPod", mp.Name)
	log.Info("Starting monitor loop")
	defer log.Info("Stopping monitor loop")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	activePodMonitors := make(map[string]context.CancelFunc)
	var podMu sync.Mutex
	for {
		select {
		case <-ctx.Done():
			podMu.Lock()
			for _, cancel := range activePodMonitors {
				cancel()
			}
			podMu.Unlock()
			return
		case <-ticker.C:
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
			for podKey, cancel := range activePodMonitors {
				if _, exists := currentPods[podKey]; !exists {
					log.Info("Stopping monitor for pod that no longer exists or matches", "podKey", podKey)
					cancel()
					delete(activePodMonitors, podKey)
				}
			}
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

// streamPodLogs 函数处理日志流
func (m *MonitorManager) streamPodLogs(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, pod *corev1.Pod) {
	log := logf.FromContext(ctx).WithValues("Pod", pod.Name)
	log.Info("Starting log stream with multiline support")
	defer log.Info("Closing log stream")
	var multilineRegex *regexp.Regexp
	var err error
	if mp.Spec.Multiline != nil {
		multilineRegex, err = regexp.Compile(mp.Spec.Multiline.Pattern)
		if err != nil {
			log.Error(err, "Invalid multiline regex pattern, falling back to single-line mode")
			multilineRegex = nil
		}
	}
	containerName := pod.Spec.Containers[0].Name
	req := m.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: containerName, Follow: true})
	stream, err := req.Stream(ctx)
	if err != nil {
		log.Error(err, "Failed to open log stream")
		return
	}
	defer stream.Close()
	scanner := bufio.NewScanner(stream)
	var buffer bytes.Buffer
	flushTimer := time.NewTimer(500 * time.Millisecond)
	flushTimer.Stop()
	processBuffer := func() {
		if buffer.Len() > 0 {
			fullLogEvent := buffer.String()
			for _, rule := range mp.Spec.Rules {
				matched, _ := regexp.MatchString(rule.Regex, fullLogEvent)
				if matched {
					log.Info("Multiline log event matched rule", "rule", rule.Name)
					go m.dispatchAlert(ctx, mp, pod, fullLogEvent, rule, containerName)
					break
				}
			}
			buffer.Reset()
		}
	}
	for {
		select {
		case <-ctx.Done():
			processBuffer()
			log.Info("Log stream context done, shutting down.")
			return
		case <-flushTimer.C:
			log.Info("Multiline buffer flushed due to timeout")
			processBuffer()
		default:
			if !scanner.Scan() {
				processBuffer()
				if err := scanner.Err(); err != nil {
					if errors.Is(err, context.Canceled) {
						log.Info("Log stream scanner stopped due to context cancellation (expected behavior).")
					} else {
						log.Error(err, "Error reading from log stream")
					}
				}
				return
			}
			line := scanner.Text()
			if multilineRegex == nil {
				buffer.WriteString(line)
				processBuffer()
				continue
			}
			isMatch := multilineRegex.MatchString(line)
			isNewEvent := (isMatch != mp.Spec.Multiline.Negate)
			if isNewEvent {
				processBuffer()
			}
			if buffer.Len() > 0 {
				buffer.WriteString("\n")
			}
			buffer.WriteString(line)
			flushTimer.Reset(500 * time.Millisecond)
		}
	}
}

func sanitizeForKubernetes(name string) string {
	// 转换为小写
	sanitized := strings.ToLower(name)
	// 将所有不符合 RFC 1123 规范的字符替换为 -
	reg := regexp.MustCompile("[^a-z0-9-]+")
	sanitized = reg.ReplaceAllString(sanitized, "-")
	// 去除开头和结尾的 -
	sanitized = strings.Trim(sanitized, "-")
	// 确保长度不超过63个字符 (Kubernetes 标签的长度限制)
	if len(sanitized) > 63 {
		sanitized = sanitized[:63]
	}
	return sanitized
}

// dispatchAlert 函数处理告警
func (m *MonitorManager) dispatchAlert(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, pod *corev1.Pod, logLine string, rule smartlogv1alpha1.LogRule, containerName string) {
	baseLog := logf.FromContext(ctx).WithValues("MonitorPod", mp.Name, "Pod", pod.Name, "Rule", rule.Name)

	if mp.Spec.RateLimit != nil && mp.Spec.RateLimit.Limit > 0 {
		rateLimitKey := fmt.Sprintf("%s/%s/%s", mp.Namespace, mp.Name, rule.Name)
		period, err := time.ParseDuration(mp.Spec.RateLimit.Period)
		if err != nil {
			baseLog.Error(err, "无效的频率限制周期格式，使用默认值 5m")
			period = 5 * time.Minute
		}
		if !m.rateLimiter.Allow(rateLimitKey, mp.Spec.RateLimit.Limit, period) {
			baseLog.Info("告警已被频率限制器抑制")
			return
		}
		baseLog.Info("频率限制检查通过，准备发送告警")
	}

	alerts, err := m.getAlertsFromTarget(ctx, &mp.Spec.AlertTarget, mp.Namespace)
	if err != nil {
		baseLog.Error(err, "获取告警渠道配置失败")
		return
	}
	if len(alerts) == 0 {
		baseLog.Info("未找到可用的告警渠道，跳过通知")
		return
	}

	escapedLogLineBytes, _ := json.Marshal(logLine)
	escapedLogLine := string(escapedLogLineBytes[1 : len(escapedLogLineBytes)-1])

	templateData := TemplateData{
		PodName:       pod.Name,
		Namespace:     pod.Namespace,
		ContainerName: containerName,
		LogLine:       escapedLogLine,
		RuleName:      rule.Name,
		Timestamp:     time.Now(),
	}

	var wg sync.WaitGroup
	successCh := make(chan bool, len(alerts))

	for _, alert := range alerts {
		wg.Add(1)
		go func(alertConf smartlogv1alpha1.Alert) {
			defer wg.Done()
			alertLog := baseLog.WithValues("alert", alertConf.Name)

			// 根据告警类型分发
			switch alertConf.Spec.Type {
			case "Webhook":
				m.sendWebhookAlert(ctx, alertLog, mp, &alertConf, templateData, successCh)
			case "Feishu":
				// 飞书使用原始日志，因为它自己的 markdown 会处理
				feishuTemplateData := templateData
				feishuTemplateData.LogLine = logLine
				m.sendFeishuAlert(ctx, alertLog, mp, &alertConf, feishuTemplateData, successCh)
			default:
				alertLog.Error(fmt.Errorf("unsupported alert type"), "Unsupported alert type", "type", alertConf.Spec.Type)
			}
		}(alert)
	}

	wg.Wait()
	close(successCh)

	if len(successCh) > 0 {
		if mp.Spec.RecordAlerts {
			baseLog.Info("RecordAlerts 已启用，正在创建 AlertRecord...")
			sanitizedRuleName := sanitizeForKubernetes(rule.Name)
			alertRecord := &smartlogv1alpha1.AlertRecord{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: fmt.Sprintf("%s-%s-", mp.Name, sanitizedRuleName),
					Namespace:    mp.Namespace,
					Labels: map[string]string{
						"smartlog.smart-tools.com/monitorpod": mp.Name,
						"smartlog.smart-tools.com/rule":       sanitizedRuleName,
					},
				},
				Spec: smartlogv1alpha1.AlertRecordSpec{
					MonitorPodRef: smartlogv1alpha1.MonitorPodReference{Name: mp.Name, Namespace: mp.Namespace},
					RuleName:      rule.Name,
					TriggeredAt:   metav1.Now(),
					SourcePod:     smartlogv1alpha1.PodReference{Name: pod.Name, Namespace: pod.Namespace, Container: containerName},
					LogSnippet:    logLine,
				},
			}
			if mp.Spec.AlertRecordTTL != "" {
				alertRecord.Annotations = map[string]string{"smartlog.smart-tools.com/ttl": mp.Spec.AlertRecordTTL}
			}
			if err := ctrl.SetControllerReference(mp, alertRecord, m.Scheme); err != nil {
				baseLog.Error(err, "设置 AlertRecord 的 Owner Reference 失败")
			} else {
				if err := m.Create(context.Background(), alertRecord); err != nil {
					baseLog.Error(err, "创建 AlertRecord 失败")
				} else {
					baseLog.Info("成功创建 AlertRecord", "AlertRecordName", alertRecord.Name)
				}
			}
		}
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

// getSecretValue 是一个辅助函数，用于从 Secret 中安全地获取值
func (m *MonitorManager) getSecretValue(ctx context.Context, namespace string, selector *corev1.SecretKeySelector) string {
	if selector == nil {
		return ""
	}
	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: namespace, Name: selector.Name}
	if err := m.Get(ctx, secretKey, &secret); err != nil {
		return ""
	}
	if val, ok := secret.Data[selector.Key]; ok {
		return string(val)
	}
	return ""
}

// sendWebhookAlert 负责发送通用的 Webhook 告警
func (m *MonitorManager) sendWebhookAlert(ctx context.Context, alertLog logr.Logger, mp *smartlogv1alpha1.MonitorPod, alertConf *smartlogv1alpha1.Alert, templateData TemplateData, successCh chan<- bool) {
	if alertConf.Spec.Webhook == nil {
		alertLog.Error(fmt.Errorf("webhook spec is nil"), "Webhook 配置缺失")
		return
	}

	templateStr := mp.Spec.AlertTemplate
	if templateStr == "" {
		templateStr = alertConf.Spec.Webhook.BodyTemplate
	}

	messageBody, err := m.renderAlertTemplate(templateStr, templateData)
	if err != nil {
		alertLog.Error(err, "渲染告警模板失败")
		return
	}

	webhookURL := m.getSecretValue(ctx, alertConf.Namespace, &alertConf.Spec.Webhook.URLSecretRef)
	if webhookURL == "" {
		alertLog.Error(fmt.Errorf("webhook url is empty"), "获取 Webhook 的 Secret 失败")
		return
	}

	reqCtx, cancelReq := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelReq()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewBuffer(messageBody))
	if err != nil {
		alertLog.Error(err, "创建 HTTP 请求失败")
		return
	}

	req.Header.Set("Content-Type", "application/json")
	for _, header := range alertConf.Spec.Webhook.Headers {
		req.Header.Set(header.Name, header.Value)
	}

	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		alertLog.Error(err, "发送 Webhook 通知失败")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		alertLog.Error(fmt.Errorf("Webhook 服务端返回错误状态码: %s", resp.Status), "Webhook 发送失败")
		return
	}

	alertLog.Info("成功发送通知")
	successCh <- true
}

// genFeishuSign 计算飞书机器人的签名
func genFeishuSign(secret string, timestamp int64) (string, error) {
	stringToSign := fmt.Sprintf("%v\n%s", timestamp, secret)
	var data []byte
	h := hmac.New(sha256.New, []byte(stringToSign))
	_, err := h.Write(data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// sendFeishuAlert 负责发送飞书告警
func (m *MonitorManager) sendFeishuAlert(ctx context.Context, alertLog logr.Logger, mp *smartlogv1alpha1.MonitorPod, alertConf *smartlogv1alpha1.Alert, templateData TemplateData, successCh chan<- bool) {
	if alertConf.Spec.Feishu == nil {
		alertLog.Error(fmt.Errorf("feishu spec is nil"), "飞书配置缺失")
		return
	}

	webhookURL := m.getSecretValue(ctx, alertConf.Namespace, &alertConf.Spec.Feishu.URLSecretRef)
	if webhookURL == "" {
		alertLog.Error(fmt.Errorf("webhook url is empty"), "获取飞书 Webhook 的 Secret 失败")
		return
	}

	// 构造飞书消息体 (使用富文本卡片)
	// 注意：飞书的 lark_md 对 json 转义的 `\` 处理不佳，所以这里使用原始 logLine
	feishuPayload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"config": map[string]bool{"wide_screen_mode": true},
			"header": map[string]interface{}{
				"title":    map[string]string{"tag": "plain_text", "content": "🔥 Smart-Log 实时告警"},
				"template": "red",
			},
			"elements": []interface{}{
				map[string]interface{}{
					"tag": "div",
					"fields": []interface{}{
						map[string]interface{}{
							"is_short": true,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**🔈 通知人员:** %s", "<at id=all></at>"),
							},
						},
						map[string]interface{}{
							"is_short": true,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**📝 告警规则:** **%s**", templateData.RuleName),
							},
						},
						map[string]interface{}{
							"is_short": true,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**📄 监控任务:** **%s**", mp.Name),
							},
						},
						map[string]interface{}{
							"is_short": true,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**📦 Pod:** **%s**", templateData.PodName),
							},
						},
						map[string]interface{}{
							"is_short": true,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**🖥️ 容器:** **%s**", templateData.ContainerName),
							},
						},
						map[string]interface{}{
							"is_short": false,
							"text": map[string]string{
								"tag":     "lark_md",
								"content": fmt.Sprintf("**🌐 命名空间:** **%s**", templateData.Namespace),
							},
						},
					},
				},
				map[string]interface{}{"tag": "hr"},
				map[string]interface{}{
					"tag": "div",
					"text": map[string]string{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**📑 日志内容:** \n\n<font color='red'>%s</font>\n", templateData.LogLine),
					},
				},
				map[string]interface{}{"tag": "hr"},
				map[string]interface{}{
					"tag": "note",
					"elements": []interface{}{
						map[string]string{
							"tag":     "plain_text",
							"content": "触发于: " + templateData.Timestamp.Format("2006-01-02 15:04:05"),
						},
					},
				},
			},
		},
	}

	// 处理加签
	if alertConf.Spec.Feishu.SecretKeySecretRef != nil {
		secretKey := m.getSecretValue(ctx, alertConf.Namespace, alertConf.Spec.Feishu.SecretKeySecretRef)
		if secretKey != "" {
			timestamp := time.Now().Unix()
			sign, err := genFeishuSign(secretKey, timestamp)
			if err != nil {
				alertLog.Error(err, "生成飞书签名失败")
			} else {
				// 飞书的签名信息需要放在 body 里
				feishuPayload["timestamp"] = timestamp
				feishuPayload["sign"] = sign
			}
		}
	}

	payloadBytes, _ := json.Marshal(feishuPayload)
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelReq()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		alertLog.Error(err, "创建 HTTP 请求失败")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		alertLog.Error(err, "发送飞书通知失败")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		alertLog.Error(fmt.Errorf("飞书服务端返回错误状态码: %s", resp.Status), "飞书发送失败")
		return
	}

	alertLog.Info("成功发送飞书通知")
	successCh <- true
}

// getAlertsFromTarget 获取指定目标下的所有已就绪的告警
func (m *MonitorManager) getAlertsFromTarget(ctx context.Context, target *smartlogv1alpha1.AlertTarget, namespace string) ([]smartlogv1alpha1.Alert, error) {
	readyAlerts := make([]smartlogv1alpha1.Alert, 0)

	if target.Kind == "Alert" {
		var alert smartlogv1alpha1.Alert
		key := types.NamespacedName{Name: target.Name, Namespace: namespace}
		if err := m.Get(ctx, key, &alert); err != nil {
			return nil, err
		}
		if meta.IsStatusConditionTrue(alert.Status.Conditions, "Ready") {
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
				continue
			}
			if meta.IsStatusConditionTrue(alert.Status.Conditions, "Ready") {
				readyAlerts = append(readyAlerts, alert)
			}
		}
	} else {
		return nil, fmt.Errorf("unsupported alert target kind: %s", target.Kind)
	}

	return readyAlerts, nil
}

// renderAlertTemplate 渲染告警模板
func (m *MonitorManager) renderAlertTemplate(templateStr string, data TemplateData) ([]byte, error) {
	if templateStr == "" {
		templateStr = `{"pod":"{{.PodName}}","namespace":"{{.Namespace}}","container":"{{.ContainerName}}","rule":"{{.RuleName}}","log":"{{.LogLine}}","timestamp":"{{.Timestamp.Format "2006-01-02T15:04:05Z07:00"}}"}`
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

func (r *MonitorPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.MonitorPod{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findMonitorPodsForPod),
		).
		Complete(r)
}

func (r *MonitorPodReconciler) findMonitorPodsForPod(ctx context.Context, podObj client.Object) []reconcile.Request {
	pod, ok := podObj.(*corev1.Pod)
	if !ok {
		return []reconcile.Request{}
	}
	var monitorPods smartlogv1alpha1.MonitorPodList
	if err := r.List(ctx, &monitorPods, client.InNamespace(pod.GetNamespace())); err != nil {
		return []reconcile.Request{}
	}
	requests := make([]reconcile.Request, 0)
	for _, mp := range monitorPods.Items {
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
