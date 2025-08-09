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

// AIProviderSpec 定义了如何连接到一个 AI 大语言模型服务。
type AIProviderSpec struct {
	// Type 指定了 AI 提供商的类型。
	// +kubebuilder:validation:Enum=Gemini;OpenAI
	Type string `json:"type"`

	// Gemini 定义了连接到 Google Gemini API 的配置。
	// +optional
	Gemini *GeminiProviderSpec `json:"gemini,omitempty"`

	// OpenAI 定义了连接到 OpenAI API 的配置。
	// +optional
	OpenAI *OpenAIProviderSpec `json:"openAI,omitempty"`
}

// GeminiProviderSpec 包含了连接 Gemini 所需的配置。
type GeminiProviderSpec struct {
	// APIKeySecretRef 引用一个包含 Gemini API Key 的 Secret。
	APIKeySecretRef corev1.SecretKeySelector `json:"apiKeySecretRef"`

	// BaseURL (可选) 用于指定一个自定义的 Gemini API 端点地址，用于代理等场景。
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Model (可选) 指定要使用的具体模型，默认为 "gemini-pro"。
	// +optional
	Model string `json:"model,omitempty"`
}

// OpenAIProviderSpec 包含了连接 OpenAI 所需的配置。
type OpenAIProviderSpec struct {
	// APIKeySecretRef 引用一个包含 OpenAI API Key 的 Secret。
	APIKeySecretRef corev1.SecretKeySelector `json:"apiKeySecretRef"`

	// BaseURL (可选) 用于指定一个自定义的 OpenAI API 端点地址，用于代理等场景。
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Model (可选) 指定要使用的具体模型，默认为 "gpt-3.5-turbo"。
	// +optional
	Model string `json:"model,omitempty"`
}

// AIProviderStatus 定义了 AIProvider 的观测状态。
type AIProviderStatus struct {
	// Conditions 存储了资源的详细状态列表，例如 Ready 状态。
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=aiproviders,shortName=aip
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AIProvider is the Schema for the aiproviders API
type AIProvider struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of AIProvider
	// +required
	Spec AIProviderSpec `json:"spec"`

	// status defines the observed state of AIProvider
	// +optional
	Status AIProviderStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// AIProviderList contains a list of AIProvider
type AIProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIProvider{}, &AIProviderList{})
}
