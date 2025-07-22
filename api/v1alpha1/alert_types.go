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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AlertSpec defines the desired state of Alert
type AlertSpec struct {
	// Type 指定了告警渠道的类型。
	// 当前版本仅支持 "Webhook"。
	// +kubebuilder:validation:Enum=Webhook
	Type string `json:"type"`

	// Webhook 的详细配置, 仅当 Type 为 "Webhook" 时有效。
	// +optional
	Webhook *WebhookSpec `json:"webhook,omitempty"`
}

// WebhookSpec 定义了发送到 Webhook 所需的配置。
type WebhookSpec struct {
	// URLSecretRef 引用一个包含 Webhook URL 的 Secret。
	URLSecretRef corev1.SecretKeySelector `json:"urlSecretRef"`
	// Headers (可选) 发送到 Webhook 的自定义 HTTP 请求头。
	// +optional
	Headers []WebhookHeader `json:"headers,omitempty"`
	// BodyTemplate (可选) 告警内容的 Go 模板, 作为渠道的默认格式。
	// +optional
	BodyTemplate string `json:"bodyTemplate,omitempty"`
}

// WebhookHeader 定义了要发送的自定义 HTTP 请求头。
type WebhookHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AlertStatus defines the observed state of Alert.
type AlertStatus struct {
	// Conditions 存储了资源的详细状态列表，例如 Ready 状态。
	// 这是表达资源状态的标准方式。
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=alerts,scope=Namespaced,shortName=alt
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="表示该告警渠道是否就绪"
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type",description="告警渠道的类型"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Alert is the Schema for the alerts API
type Alert struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of Alert
	// +required
	Spec AlertSpec `json:"spec"`

	// status defines the observed state of Alert
	// +optional
	Status AlertStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AlertList contains a list of Alert
type AlertList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Alert `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Alert{}, &AlertList{})
}
