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
	Selector metav1.LabelSelector `json:"selector"`

	// ContainerNames 用于过滤监控的容器
	ContainerNames []string `json:"containerNames,omitempty"`

	// Rules 定义需要匹配的日志行
	Rules []LogRule `json:"rules"`

	// AlertTarget 定义发送告警的目标, 这个目标可以是 Alert 或者 AlertGroup
	AlertTarget AlertTarget `json:"alertTarget"`

	// AlertTemplate 告警模板
	AlertTemplate string `json:"alertTemplate,omitempty"`

	// RateLimit 用于限制发送的告警数量
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`
}

type LogRule struct {
	// Name 用于指定规则名称
	Name string `json:"name"`
	// Regex 用于匹配日志行
	Regex string `json:"regex"`
	// ExcludeRegex 用于排除匹配的日志行
	ExcludeRegex string `json:"excludeRegex,omitempty"`
}

type AlertTarget struct {
	// Kind 用于指定目标类型，可以是 "Alert" 或 "AlertGroup"
	Kind string `json:"kind"`
	// Name 用于指定目标名称
	Name string `json:"name"`
}

type RateLimitSpec struct {
	// Period 用于指定限制的频率, 例如 "1m" 表示一分钟内最多发送一次
	Period string `json:"period"`
	// Limit 用于指定限制的次数, 例如 10 表示一分钟内最多发送 10 条告警
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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=monitorpods,scope=Namespaced,shortName=mp
// +kubebuilder:printcolumn:name="MonitoredPodsCount",type="integer",JSONPath=".status.monitoredPodsCount",description="The number of pods currently being monitored"
// +kubebuilder:printcolumn:name="AlertsSentCount",type="integer",JSONPath=".status.alertsSentCount",description="Total number of alerts sent"
// +kubebuilder:printcolumn:name="LastTriggeredTime",type="date",JSONPath=".status.lastTriggeredTime",description="The last time an alert was triggered"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age of the object"

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
