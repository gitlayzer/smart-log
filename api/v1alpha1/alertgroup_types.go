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

// AlertGroupSpec 定义了 AlertGroup 资源的期望状态。
type AlertGroupSpec struct {
	// AlertNames 是一个列表，包含了属于该组的 Alert 资源的名称。
	// 这些 Alert 必须与 AlertGroup 位于同一个命名空间。
	// +kubebuilder:validation:MinItems=1
	AlertNames []string `json:"alertNames"`
}

// AlertGroupStatus 定义了 AlertGroup 资源的观测状态。
type AlertGroupStatus struct {
	// TotalAlerts 是组内定义的告警渠道总数。
	// +optional
	TotalAlerts int `json:"totalAlerts,omitempty"`

	// ReadyAlerts 是组内已就绪（验证通过）的告警渠道数量。
	// +optional
	ReadyAlerts int `json:"readyAlerts,omitempty"`

	// Conditions 存储了 AlertGroup 的详细状态列表。
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=alertgroups,scope=Namespaced,shortName=ag
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="表示该告警组是否就绪"
// +kubebuilder:printcolumn:name="Total",type="integer",JSONPath=".status.totalAlerts",description="组内告警渠道总数"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.readyAlerts",description="组内可用的告警渠道数量"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AlertGroup is the Schema for the alertgroups API
type AlertGroup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of AlertGroup
	// +required
	Spec AlertGroupSpec `json:"spec"`

	// status defines the observed state of AlertGroup
	// +optional
	Status AlertGroupStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AlertGroupList contains a list of AlertGroup
type AlertGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AlertGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AlertGroup{}, &AlertGroupList{})
}
