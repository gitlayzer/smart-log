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
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/go-logr/logr"

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

type MonitorPodReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Manager *MonitorManager
}

type MonitorManager struct {
	mu       sync.RWMutex
	monitors map[string]context.CancelFunc

	client.Client
	clientset   *kubernetes.Clientset
	rateLimiter *InMemoryRateLimiter
	Scheme      *runtime.Scheme
}

type RateLimiterInfo struct {
	Count      int
	LastSentAt time.Time
}

type InMemoryRateLimiter struct {
	mu   sync.Mutex
	data map[string]RateLimiterInfo
}

type AIData struct {
	Summary     string   `json:"summary"`
	RootCause   string   `json:"rootCause"`
	Suggestions []string `json:"suggestions"`
}

type TemplateData struct {
	PodName       string
	Namespace     string
	ContainerName string
	LogLine       string
	RuleName      string
	Timestamp     time.Time
	AI            *AIData
}

func NewInMemoryRateLimiter() *InMemoryRateLimiter {
	return &InMemoryRateLimiter{
		data: make(map[string]RateLimiterInfo),
	}
}

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

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=aiproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=monitorpods/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alerts,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertrecords,verbs=create

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

	if err = r.List(ctx, &podList, client.InNamespace(monitorPod.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
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

func (m *MonitorManager) StopMonitor(namespacedName types.NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := namespacedName.String()
	if cancel, exists := m.monitors[key]; exists {
		cancel()
		delete(m.monitors, key)
	}
}

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

func (m *MonitorManager) sendWebhookAlert(ctx context.Context, alertLog logr.Logger, mp *smartlogv1alpha1.MonitorPod, alertConf *smartlogv1alpha1.Alert, templateData TemplateData, successCh chan<- bool) {
	if alertConf.Spec.Webhook == nil {
		alertLog.Error(fmt.Errorf("webhook spec is nil"), "Webhook 配置缺失")
		return
	}
	templateStr := mp.Spec.AlertTemplate
	if templateStr == "" {
		templateStr = alertConf.Spec.Webhook.BodyTemplate
	}

	webhookTemplateData := templateData
	escapedLogLineBytes, _ := json.Marshal(templateData.LogLine)
	webhookTemplateData.LogLine = string(escapedLogLineBytes[1 : len(escapedLogLineBytes)-1])

	messageBody, err := m.renderAlertTemplate(templateStr, &webhookTemplateData)
	if err != nil {
		alertLog.Error(err, "渲染告警模板失败")
		return
	}

	webhookURL := m.getSecretValue(ctx, alertConf.Namespace, &alertConf.Spec.Webhook.URLSecretRef)
	if webhookURL == "" {
		alertLog.Error(fmt.Errorf("webhook url is empty"), "获取 Webhook 的 Secret 失败")
		return
	}

	var resp *http.Response
	const maxRetries = 3
	const retrySleep = 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			alertLog.Info("Retrying webhook request", "attempt", attempt+1, "after", (retrySleep * time.Duration(attempt)).String())
			time.Sleep(retrySleep * time.Duration(attempt))
		}

		reqCtx, cancelReq := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelReq()

		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewReader(messageBody))
		if reqErr != nil {
			alertLog.Error(reqErr, "创建 HTTP 请求失败")
			return
		}

		req.Header.Set("Content-Type", "application/json")
		for _, header := range alertConf.Spec.Webhook.Headers {
			req.Header.Set(header.Name, header.Value)
		}

		httpClient := &http.Client{}
		resp, err = httpClient.Do(req)

		if err == nil && resp.StatusCode < 500 {
			break
		}

		if err != nil {
			alertLog.Error(err, "Webhook request attempt failed")
		} else if resp != nil {
			alertLog.Info("Webhook request attempt received server error", "status", resp.Status)
			resp.Body.Close()
		}
	}

	if err != nil {
		alertLog.Error(err, fmt.Sprintf("发送 Webhook 通知失败，已尝试 %d 次", maxRetries))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		alertLog.Error(fmt.Errorf("webhook 服务端返回错误状态码: %s，已尝试 %d 次", resp.Status, maxRetries), "Webhook 发送失败")
		return
	}

	alertLog.Info("成功发送通知")
	successCh <- true
}

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

func buildAIElements(aiData *AIData) []interface{} {
	// 格式化修复建议
	var suggestionsBuilder strings.Builder
	for i, s := range aiData.Suggestions {
		// 在每个建议前加上项目符号
		suggestionsBuilder.WriteString(fmt.Sprintf("\n> %d. %s", i+1, s))
	}

	// 返回一个包含多个元素的切片，代表整个 AI 分析模块
	return []interface{}{
		// 1. 分隔线
		map[string]interface{}{"tag": "hr"},
		// 2. AI 分析的标题
		map[string]interface{}{
			"tag": "div",
			"text": map[string]string{
				"tag":     "lark_md",
				"content": "**🤖 AI 智能分析**",
			},
		},
		// 3. 使用 "fields" 结构化展示摘要、原因和建议
		map[string]interface{}{
			"tag": "div",
			"fields": []interface{}{
				map[string]interface{}{
					"is_short": false, // 单独占一行，内容更清晰
					"text": map[string]string{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**摘要:**\n%s", aiData.Summary),
					},
				},
				map[string]interface{}{
					"is_short": false,
					"text": map[string]string{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**根本原因:**\n%s", aiData.RootCause),
					},
				},
				map[string]interface{}{
					"is_short": false,
					"text": map[string]string{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**修复建议:**%s", suggestionsBuilder.String()),
					},
				},
			},
		},
	}
}

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

	feishuPayload := m.buildFeishuCardPayload(mp, templateData)

	// 为 payload 增加签名（如果需要）
	if alertConf.Spec.Feishu.SecretKeySecretRef != nil {
		secretKey := m.getSecretValue(ctx, alertConf.Namespace, alertConf.Spec.Feishu.SecretKeySecretRef)
		if secretKey != "" {
			timestamp := time.Now().Unix()
			sign, err := genFeishuSign(secretKey, timestamp)
			if err != nil {
				alertLog.Error(err, "生成飞书签名失败")
			} else {
				feishuPayload["timestamp"] = timestamp
				feishuPayload["sign"] = sign
			}
		}
	}

	payloadBytes, _ := json.Marshal(feishuPayload)

	var resp *http.Response
	var err error
	const maxRetries = 3
	const retrySleep = 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			alertLog.Info("Retrying Feishu send request", "attempt", attempt+1)
			time.Sleep(retrySleep * time.Duration(attempt))
		}

		reqCtx, cancelReq := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelReq()

		req, reqErr := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewReader(payloadBytes))
		if reqErr != nil {
			alertLog.Error(reqErr, "创建 HTTP 请求失败")
			return
		}
		req.Header.Set("Content-Type", "application/json")

		httpClient := &http.Client{}
		resp, err = httpClient.Do(req)

		if err == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	if err != nil {
		alertLog.Error(err, fmt.Sprintf("发送飞书通知失败，已尝试 %d 次", maxRetries))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		alertLog.Error(fmt.Errorf("飞书服务端返回错误状态码: %s, body: %s", resp.Status, string(bodyBytes)), "飞书发送失败")
		return
	}

	alertLog.Info("成功发送飞书通知")
	successCh <- true
}

func (m *MonitorManager) dispatchAlert(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, pod *corev1.Pod, logLine string, rule smartlogv1alpha1.LogRule, containerName string) {
	baseLog := logf.FromContext(ctx).WithValues("MonitorPod", mp.Name, "Pod", pod.Name, "Rule", rule.Name)

	// 1. 频率限制检查
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
	}

	// 2. 获取告警目标
	alerts, err := m.getAlertsFromTarget(ctx, &mp.Spec.AlertTarget, mp.Namespace)
	if err != nil {
		baseLog.Error(err, "获取告警渠道配置失败")
		return
	}
	if len(alerts) == 0 {
		baseLog.Info("未找到可用的告警渠道，跳过通知")
		return
	}

	// 3. 准备基础模板数据 (不含AI)
	templateData := TemplateData{
		PodName:       pod.Name,
		Namespace:     pod.Namespace,
		ContainerName: containerName,
		LogLine:       logLine,
		RuleName:      rule.Name,
		Timestamp:     time.Now(),
		AI:            nil,
	}

	// 4. --- 新逻辑：带超时的同步AI分析 ---
	if mp.Spec.AIEnrichment != nil && mp.Spec.AIEnrichment.Enabled {
		baseLog.Info("AI enrichment enabled, attempting to analyze log with timeout...")

		// --- 这是修改点：从MonitorPod Spec中读取超时时间 ---
		// 设置一个更合理的默认超时时间
		timeout := 10 * time.Second

		if mp.Spec.AIEnrichment.Timeout != "" {
			parsedTimeout, err := time.ParseDuration(mp.Spec.AIEnrichment.Timeout)
			if err != nil {
				// 如果格式错误，打印日志并使用默认值
				baseLog.Error(err, "Invalid AI enrichment timeout format, using default", "configuredValue", mp.Spec.AIEnrichment.Timeout)
			} else {
				timeout = parsedTimeout
			}
		}

		baseLog.Info("Using AI enrichment timeout", "timeout", timeout.String())

		// 为AI分析创建一个带超时的上下文，例如5秒
		aiCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		aiData, err := m.enrichWithAI(aiCtx, mp, &templateData)
		if err != nil {
			// 如果错误是超时引起的，就打印Info日志，否则打印Error日志
			if errors.Is(err, context.DeadlineExceeded) {
				baseLog.Info("AI enrichment timed out, sending alert without AI analysis.")
			} else {
				baseLog.Error(err, "AI enrichment failed, sending alert without AI analysis.")
			}
		}

		templateData.AI = aiData
	}

	var wg sync.WaitGroup
	successCh := make(chan bool, len(alerts))

	for _, alert := range alerts {
		wg.Add(1)
		go func(alertConf smartlogv1alpha1.Alert) {
			defer wg.Done()
			alertLog := baseLog.WithValues("alert", alertConf.Name)

			switch alertConf.Spec.Type {
			case "Webhook":
				m.sendWebhookAlert(ctx, alertLog, mp, &alertConf, templateData, successCh)
			case "Feishu":
				m.sendFeishuAlert(ctx, alertLog, mp, &alertConf, templateData, successCh)
			default:
				alertLog.Error(fmt.Errorf("unsupported alert type"), "Unsupported alert type", "type", alertConf.Spec.Type)
			}
		}(alert)
	}

	wg.Wait()
	close(successCh)

	// 5. 只要有任何一个初始告警发送成功，就记录和更新状态
	if len(successCh) > 0 {
		// --- 补充完整的 AlertRecord 创建逻辑 ---
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
			if err = ctrl.SetControllerReference(mp, alertRecord, m.Scheme); err != nil {
				baseLog.Error(err, "设置 AlertRecord 的 Owner Reference 失败")
			} else {
				if err = m.Create(context.Background(), alertRecord); err != nil {
					baseLog.Error(err, "创建 AlertRecord 失败")
				} else {
					baseLog.Info("成功创建 AlertRecord", "AlertRecordName", alertRecord.Name)
				}
			}
		}

		// --- 补充完整的 MonitorPod 状态更新逻辑 ---
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

func (m *MonitorManager) buildFeishuCardPayload(mp *smartlogv1alpha1.MonitorPod, templateData TemplateData) map[string]interface{} {
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
						map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**🔈 通知人员:** %s", "<at id=all></at>")}},
						map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**📝 告警规则:** **%s**", templateData.RuleName)}},
						map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**📄 监控任务:** **%s**", mp.Name)}},
						map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**📦 Pod:** **%s**", templateData.PodName)}},
						map[string]interface{}{"is_short": true, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**🖥️ 容器:** **%s**", templateData.ContainerName)}},
						map[string]interface{}{"is_short": false, "text": map[string]string{"tag": "lark_md", "content": fmt.Sprintf("**🌐 命名空间:** **%s**", templateData.Namespace)}},
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
			},
		},
	}

	card := feishuPayload["card"].(map[string]interface{})
	elements := card["elements"].([]interface{})

	if templateData.AI != nil {
		aiElements := buildAIElements(templateData.AI)
		elements = append(elements, aiElements...)
	}

	finalElements := []interface{}{
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
	}
	elements = append(elements, finalElements...)
	card["elements"] = elements

	return feishuPayload
}

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

func (m *MonitorManager) renderAlertTemplate(templateStr string, data *TemplateData) ([]byte, error) {
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

func (m *MonitorManager) enrichWithAI(ctx context.Context, mp *smartlogv1alpha1.MonitorPod, data *TemplateData) (*AIData, error) {
	// 1. 获取 AIProvider
	var provider smartlogv1alpha1.AIProvider
	// AIProvider 是命名空间级别的，我们假设它与 MonitorPod 在同一个命名空间
	providerKey := client.ObjectKey{Name: mp.Spec.AIEnrichment.ProviderRef.Name, Namespace: mp.Namespace}
	if err := m.Get(ctx, providerKey, &provider); err != nil {
		return nil, fmt.Errorf("failed to get referenced AIProvider %s: %w", providerKey.Name, err)
	}

	// 2. 检查 AIProvider 是否就绪
	if !meta.IsStatusConditionTrue(provider.Status.Conditions, "Ready") {
		return nil, fmt.Errorf("referenced AIProvider %s is not ready", provider.Name)
	}

	promptStr := mp.Spec.AIEnrichment.Prompt
	if promptStr == "" {
		// 如果用户没有提供 Prompt，则使用一个高质量的内置默认 Prompt
		promptStr = `你是一位资深的 Kubernetes SRE 专家。请分析以下来自 Pod '{{ .PodName }}' (命名空间: '{{ .Namespace }}') 的错误日志。
该日志由告警规则 '{{ .RuleName }}' 捕捉。

日志内容:
"{{ .LogLine }}"

请提供简明的错误摘要、一个最可能的根本原因、以及三条具体的、可操作的修复建议。
请**严格**以如下 JSON 格式返回你的分析结果，不要包含任何额外的解释或 markdown 标记:
{
  "summary": "一句话总结错误",
  "rootCause": "分析出的根本原因",
  "suggestions": ["建议1", "建议2", "建议3"]
}`
	}

	// 3. 渲染 Prompt
	promptTemplate, err := template.New("prompt").Parse(promptStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse AI prompt template: %w", err)
	}
	var buf bytes.Buffer
	if err = promptTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute AI prompt template: %w", err)
	}
	renderedPrompt := buf.String()

	// 4. 调用大语言模型
	analysisJSON, err := m.callLanguageModel(ctx, &provider, renderedPrompt)
	if err != nil {
		return nil, err
	}

	// 5. 解析结果
	var aiData AIData
	if err = json.Unmarshal([]byte(analysisJSON), &aiData); err != nil {
		// 尝试清理 AI 返回的 markdown 代码块
		re := regexp.MustCompile("(?s)```json\n(.*?)\n```")
		matches := re.FindStringSubmatch(analysisJSON)
		if len(matches) > 1 {
			if err = json.Unmarshal([]byte(matches[1]), &aiData); err == nil {
				return &aiData, nil
			}
		}
		return nil, fmt.Errorf("failed to parse AI model response: %w. Response: %s", err, analysisJSON)
	}

	return &aiData, nil
}

func (m *MonitorManager) callLanguageModel(ctx context.Context, provider *smartlogv1alpha1.AIProvider, prompt string) (string, error) {
	log := logf.FromContext(ctx)
	var apiKey, baseURL, model, apiURL, resultPath string
	var reqBody []byte

	switch provider.Spec.Type {
	case "Gemini":
		spec := provider.Spec.Gemini
		apiKey = m.getSecretValue(ctx, provider.Namespace, &spec.APIKeySecretRef)
		baseURL = "https://generativelanguage.googleapis.com"
		if spec.BaseURL != "" {
			baseURL = spec.BaseURL
		}
		model = "gemini-1.5-flash"
		if spec.Model != "" {
			model = spec.Model
		}
		apiURL = fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", baseURL, model, apiKey)
		payload := map[string]interface{}{"contents": []map[string]interface{}{{"parts": []map[string]string{{"text": prompt}}}}}
		reqBody, _ = json.Marshal(payload)
		resultPath = "candidates.0.content.parts.0.text"
	case "OpenAI":
		spec := provider.Spec.OpenAI
		apiKey = m.getSecretValue(ctx, provider.Namespace, &spec.APIKeySecretRef)
		baseURL = "https://api.openai.com"
		if spec.BaseURL != "" {
			baseURL = spec.BaseURL
		}
		model = "gpt-3.5-turbo"
		if spec.Model != "" {
			model = spec.Model
		}
		apiURL = fmt.Sprintf("%s/v1/chat/completions", baseURL)
		payload := map[string]interface{}{
			"model":    model,
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		}
		reqBody, _ = json.Marshal(payload)
		resultPath = "choices.0.message.content"
	default:
		return "", fmt.Errorf("unsupported AI provider type: %s", provider.Spec.Type)
	}

	if apiKey == "" {
		return "", fmt.Errorf("API key for provider %s is empty", provider.Name)
	}

	var resp *http.Response
	var err error
	const maxRetries = 3
	const retrySleep = 1 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			log.Info("Retrying AI request", "attempt", attempt+1, "after", (retrySleep * time.Duration(attempt)).String())
			time.Sleep(retrySleep * time.Duration(attempt))
		}

		// 使用 bytes.NewReader 替代 bytes.NewBuffer，因为它允许在重试时重复读取 body
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
		if reqErr != nil {
			return "", fmt.Errorf("failed to create http request: %w", reqErr) // 这是一个不可重试的错误
		}

		req.Header.Set("Content-Type", "application/json")
		if provider.Spec.Type == "OpenAI" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err = http.DefaultClient.Do(req)

		// 检查上下文是否已超时。如果是，则没有必要重试，直接返回错误。
		if errors.Is(err, context.DeadlineExceeded) {
			log.Error(err, "AI request context deadline exceeded, stopping retries.")
			return "", err
		}

		// 如果没有网络错误，且 HTTP 状态码不是 5xx 服务端错误，则认为成功，跳出重试循环
		if err == nil && resp.StatusCode < 500 {
			break
		}

		// 记录当次尝试的错误，以便调试
		if err != nil {
			log.Error(err, "AI request attempt failed")
		} else if resp != nil {
			log.Info("AI request attempt received server error", "status", resp.Status)
			resp.Body.Close() // 在重试前必须关闭旧的响应体，防止资源泄露
		}
	}

	// 在所有重试都结束后，检查最终结果
	if err != nil {
		return "", fmt.Errorf("request to AI provider failed after %d attempts: %w", maxRetries, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 { // 包括 4xx 和 5xx
		return "", fmt.Errorf("AI provider returned error status %s after %d attempts", resp.Status, maxRetries)
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", err
	}

	parts := strings.Split(resultPath, ".")
	var current interface{} = respBody
	for _, part := range parts {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else if a, ok := current.([]interface{}); ok {
			if len(a) > 0 {
				current = a[0]
			} else {
				return "", fmt.Errorf("failed to parse AI response: array is empty at path segment %s", part)
			}
		} else {
			return "", fmt.Errorf("failed to parse AI response at path: %s", resultPath)
		}
	}

	if text, ok := current.(string); ok {
		return text, nil
	}

	return "", fmt.Errorf("unexpected AI response format")
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
