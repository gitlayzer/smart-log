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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MonitorPodSpec defines the desired state of MonitorPod
type MonitorPodSpec struct {
	// Selector 用于选择要监控的 Pod
	// +optional
	Selector metav1.LabelSelector `json:"selector"`

	// ContainerNames 用于过滤监控的容器
	// +optional
	ContainerNames []string `json:"containerNames,omitempty"`

	// Rules 定义需要匹配的日志行
	// +optional
	Rules []LogRule `json:"rules"`

	// AlertTarget 定义发送告警的目标, 这个目标可以是 Alert 或者 AlertGroup
	// +optional
	AlertTarget AlertTarget `json:"alertTarget"`

	// AlertTemplate 告警模板
	// +optional
	AlertTemplate string `json:"alertTemplate,omitempty"`

	// RateLimit 用于限制发送的告警数量
	// +optional
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`

	// Multiline 用于处理多行日志
	// +optional
	Multiline *MultilineSpec `json:"multiline,omitempty"`

	// RecordAlerts 控制是否为触发的告警创建 AlertRecord 资源。
	// 默认为 false。
	// +optional
	RecordAlerts bool `json:"recordAlerts,omitempty"`

	// AlertRecordTTL 定义了创建的 AlertRecord 资源的存活时间 (Time-To-Live)。
	// 例如 "720h" (30天), "168h" (7天)。
	// 只有在 recordAlerts 为 true 时才有效。
	// +optional
	AlertRecordTTL string `json:"alertRecordTTL,omitempty"`

	// AIEnrichment (可选) 用于配置 AI 对告警内容的丰富化处理
	// +optional
	AIEnrichment *AIEnrichmentSpec `json:"aiEnrichment,omitempty"`
}

// AIEnrichmentSpec 定义了如何使用 AI 来丰富告警内容。
type AIEnrichmentSpec struct {
	// Enabled 用于启用 AI 对告警内容的丰富化处理。
	// +optional
	Enabled bool `json:"enabled"`

	// ProviderRef 引用一个 AIProvider 资源。
	// +optional
	ProviderRef ProviderReference `json:"providerRef"`

	// Prompt 是一个 Go 模板字符串，用于构建发送给大语言模型的提示。
	// +optional
	Prompt string `json:"prompt"`

	// Timeout 用于设置大语言模型调用的超时时间。
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// ProviderReference 包含了对 AIProvider 的引用信息。
type ProviderReference struct {
	// Name 是 AIProvider 资源的名称。
	// +optional
	Name string `json:"name"`
}

// MultilineSpec 定义了如何将多行日志合并成一个单一事件。
type MultilineSpec struct {
	// Pattern 是一个正则表达式，用于标识一个日志行的归属。
	// 它通常用于匹配一个新日志事件的起始行。
	// +optional
	Pattern string `json:"pattern"`

	// Negate 控制 Pattern 的匹配行为。
	// - false (默认): 匹配 Pattern 的行被视为【新事件的开始】。
	// - true: 不匹配 Pattern 的行被视为【新事件的开始】。
	// +optional
	Negate bool `json:"negate,omitempty"`
}

type LogRule struct {
	// Name 用于指定规则名称
	// +optional
	Name string `json:"name"`
	// Regex 用于匹配日志行
	// +optional
	Regex string `json:"regex"`
	// ExcludeRegex 用于排除匹配的日志行
	// +optional
	ExcludeRegex string `json:"excludeRegex,omitempty"`
}

type AlertTarget struct {
	// Kind 用于指定目标类型，可以是 "Alert" 或 "AlertGroup"
	// +kubebuilder:validation:Enum=Alert;AlertGroup
	// +optional
	Kind string `json:"kind"`
	// Name 用于指定目标名称
	// +optional
	Name string `json:"name"`
}

type RateLimitSpec struct {
	// Period 用于指定限制的频率, 例如 "1m" 表示一分钟内最多发送一次
	// +optional
	Period string `json:"period"`
	// Limit 用于指定限制的次数, 例如 10 表示一分钟内最多发送 10 条告警
	// +optional
	Limit int `json:"limit"`
}

// MonitorPodStatus defines the observed state of MonitorPod.
type MonitorPodStatus struct {
	// MonitoredPodsCount 是当前正在被监控的 Pod 的数量
	MonitoredPodsCount int `json:"monitoredPodsCount,omitempty"`
	// MonitoredPods 是当前正在被监控的 Pod 的列表
	MonitoredPods []string `json:"monitoredPods,omitempty"`
	// LastTriggeredTime 是最近一次触发告警的时间
	LastTriggeredTime *metav1.Time `json:"lastTriggeredTime,omitempty"`
	// AlertSendCount 是已经发送的告警数量
	AlertsSentCount int64 `json:"alertsSentCount,omitempty"`
	// Conditions 是当前 Pod 的状态
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// --- 【核心修复】所有 CRD 级别的注解都必须写在这里 ---
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=monitorpods,shortName=mp,scope=Namespaced
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="表示 MonitorPod 配置是否就绪"
//+kubebuilder:printcolumn:name="Monitored",type="integer",JSONPath=".status.monitoredPodsCount",description="当前监控的 Pod 数量"
//+kubebuilder:printcolumn:name="Alerts Sent",type="integer",JSONPath=".status.alertsSentCount",description="已发送的告警总数"
//+kubebuilder:printcolumn:name="Last Alert",type="date",JSONPath=".status.lastTriggeredTime",description="最近一次告警时间"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MonitorPod is the Schema for the monitorpods API
type MonitorPod struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of MonitorPod
	// +required
	Spec MonitorPodSpec `json:"spec"`

	// status defines the observed state of MonitorPod
	// +optional
	Status MonitorPodStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// MonitorPodList contains a list of MonitorPod
type MonitorPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MonitorPod `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MonitorPod{}, &MonitorPodList{})
}
