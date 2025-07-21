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

// AlertGroupSpec defines the desired state of AlertGroup
type AlertGroupSpec struct {
	// AlertNames 制定了这个 AlertGroup 所包含的 Alert 的告警渠道
	AlertNames []string `json:"alertNames"`
}

// AlertGroupStatus defines the observed state of AlertGroup.
type AlertGroupStatus struct {
	// TotalAlerts 是组内定义的告警渠道总数。
	TotalAlerts int `json:"totalAlerts,omitempty"`

	// ReadyAlerts 是组内已就绪（验证通过）的告警渠道数量。
	ReadyAlerts int `json:"readyAlerts,omitempty"`

	// Conditions 存储了 AlertGroup 的详细状态列表。
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=alertgroups,scope=Namespaced,shortName=ag
// +kubebuilder:printcolumn:name="TotalAlerts",type="integer",JSONPath=".status.totalAlerts"
// +kubebuilder:printcolumn:name="ReadyAlerts",type="integer",JSONPath=".status.readyAlerts"
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
