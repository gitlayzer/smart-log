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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// AlertSpec defines the desired state of Alert
type AlertSpec struct {
	// Type 是配置此次告警的渠道, 告警渠道都有: WeChat, DingTalk, Webhook, Email
	Type string `json:"type"`

	// 第一版当前仅支持 WebHook
	Webhook *WebhookSpec `json:"webhook,omitempty"`
}

type WebhookSpec struct {
	// 获取告警渠道的 URL
	URLSecretRef corev1.SecretKeySelector `json:"urlSecretRef"`
	// 可以选择自定义 header
	Headers []WebhookHeader `json:"headers,omitempty"`
	// 定义告警模板
	BodyTemplate string `json:"bodyTemplate,omitempty"`
}

// WebhookHeader 定义了要发送的自定义 HTTP 请求头。
type WebhookHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AlertStatus defines the observed state of Alert.
type AlertStatus struct {
	// Ready 表示该 Alert 配置是否经过验证且可用
	Ready bool `json:"ready"`
	// Conditions 存储了当前资源的状态信息
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=alerts,scope=Namespaced,shortName=alt
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready",description="The readiness of the Alert"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="The status of the Alert"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of the Alert"

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
