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

// AlertRecordSpec defines the desired state of AlertRecord
type AlertRecordSpec struct {
	// MonitorPodRef 是触发此告警的 MonitorPod 的引用。
	// +optional
	MonitorPodRef MonitorPodReference `json:"monitorPodRef"`

	// RuleName 是匹配到的规则的名称。
	// +optional
	RuleName string `json:"ruleName"`

	// TriggeredAt 是告警的精确触发时间。
	// +optional
	TriggeredAt metav1.Time `json:"triggeredAt"`

	// SourcePod 是产生日志的源头 Pod 的信息。
	// +optional
	SourcePod PodReference `json:"sourcePod"`

	// LogSnippet 是匹配到的日志片段（可以是多行）。
	// +optional
	LogSnippet string `json:"logSnippet"`
}

// MonitorPodReference 包含了对 MonitorPod 的引用信息。
type MonitorPodReference struct {
	// Name 是 MonitorPod 的名称。
	// +optional
	Name string `json:"name"`
	// Namespace 是 MonitorPod 所在的命名空间。
	// +optional
	Namespace string `json:"namespace"`
}

// PodReference 包含了对源头 Pod 的引用信息。
type PodReference struct {
	// Name 是源 Pod 的名称。
	// +optional
	Name string `json:"name"`
	// Namespace 是源 Pod 所在的命名空间。
	// +optional
	Namespace string `json:"namespace"`
	// Container 是源 Pod 中触发告警的容器名称。
	// +optional
	Container string `json:"container"`
}

// AlertRecordStatus defines the observed state of AlertRecord.
type AlertRecordStatus struct {
	// State 可以是 Firing, Ack, Resolved 等。
	// +optional
	State string `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=alertrecords,scope=Namespaced,shortName=ar,singular=alertrecord
// +kubebuilder:printcolumn:name="MonitorPod",type="string",JSONPath=".spec.monitorPodRef.name",description="The MonitorPod that triggered the alert"
// +kubebuilder:printcolumn:name="Rule",type="string",JSONPath=".spec.ruleName",description="The rule that triggered the alert"
// +kubebuilder:printcolumn:name="Source Pod",type="string",JSONPath=".spec.sourcePod.name",description="The Pod that triggered the alert"
// +kubebuilder:printcolumn:name="Triggered At",type="date",JSONPath=".spec.triggeredAt",description="The time when the alert was triggered"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of the AlertRecord"

// AlertRecord is the Schema for the alertrecords API
type AlertRecord struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of AlertRecord
	// +required
	Spec AlertRecordSpec `json:"spec"`

	// status defines the observed state of AlertRecord
	// +optional
	Status AlertRecordStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AlertRecordList contains a list of AlertRecord
type AlertRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AlertRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AlertRecord{}, &AlertRecordList{})
}
