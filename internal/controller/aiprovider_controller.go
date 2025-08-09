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
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
)

// AIProviderReconciler reconciles a AIProvider object
type AIProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=aiproviders,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=aiproviders/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=aiproviders/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AIProvider object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *AIProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// 在每次尝试更新前，都重新获取最新版本的 AIProvider 对象
		var provider smartlogv1alpha1.AIProvider
		if err := r.Get(ctx, req.NamespacedName, &provider); err != nil {
			// 如果在重试期间对象被删除，这里会返回 NotFound 错误，并停止重试
			if apierrors.IsNotFound(err) {
				logger.Info("AIProvider resource not found during status update. Ignoring.")
				return nil
			}
			return err
		}

		// 深度复制一份原始状态，用于后续比较
		originalStatus := provider.Status.DeepCopy()

		// 在最新对象上计算和设置状态
		isValid, validationErr := r.validateAIProvider(ctx, &provider)
		readyCondition := metav1.Condition{
			Type: "Ready",
		}
		if isValid {
			readyCondition.Status = metav1.ConditionTrue
			readyCondition.Reason = "ValidationSucceeded"
			readyCondition.Message = "AIProvider configuration is valid and ready"
		} else {
			readyCondition.Status = metav1.ConditionFalse
			readyCondition.Reason = "ValidationFailed"
			readyCondition.Message = validationErr.Error()
		}
		meta.SetStatusCondition(&provider.Status.Conditions, readyCondition)

		// 只有在状态发生有意义的变化时才更新
		if reflect.DeepEqual(originalStatus, &provider.Status) {
			return nil
		}

		return r.Status().Update(ctx, &provider)
	})

	if err != nil {
		logger.Error(err, "Failed to update AIProvider status after multiple retries")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *AIProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&smartlogv1alpha1.AIProvider{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findAIProvidersForSecret),
		).
		Complete(r)
}

func (r *AIProviderReconciler) validateAIProvider(ctx context.Context, provider *smartlogv1alpha1.AIProvider) (bool, error) {
	var secretRef *corev1.SecretKeySelector

	switch provider.Spec.Type {
	case "Gemini":
		if provider.Spec.Gemini == nil {
			return false, fmt.Errorf("gemini spec is not defined for provider type Gemini")
		}
		secretRef = &provider.Spec.Gemini.APIKeySecretRef
	case "OpenAI":
		if provider.Spec.OpenAI == nil {
			return false, fmt.Errorf("openAI spec is not defined for provider type OpenAI")
		}
		secretRef = &provider.Spec.OpenAI.APIKeySecretRef
	default:
		return false, fmt.Errorf("unsupported provider type: %s", provider.Spec.Type)
	}

	return r.validateSecretRef(ctx, provider.Namespace, secretRef)
}

func (r *AIProviderReconciler) validateSecretRef(ctx context.Context, namespace string, selector *corev1.SecretKeySelector) (bool, error) {
	var secret corev1.Secret
	secretKey := types.NamespacedName{Namespace: namespace, Name: selector.Name}

	if err := r.Get(ctx, secretKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("referenced secret '%s' in namespace '%s' not found", selector.Name, namespace)
		}
		return false, fmt.Errorf("failed to get referenced secret '%s': %w", selector.Name, err)
	}

	if _, ok := secret.Data[selector.Key]; !ok {
		return false, fmt.Errorf("key '%s' not found in secret '%s'", selector.Key, selector.Name)
	}
	return true, nil
}

func (r *AIProviderReconciler) findAIProvidersForSecret(ctx context.Context, secret client.Object) []reconcile.Request {
	var providers smartlogv1alpha1.AIProviderList
	if err := r.List(ctx, &providers, client.InNamespace(secret.GetNamespace())); err != nil {
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0)
	for _, provider := range providers.Items {
		var referenced bool
		switch provider.Spec.Type {
		case "Gemini":
			if provider.Spec.Gemini != nil &&
				provider.Spec.Gemini.APIKeySecretRef.Name == secret.GetName() {
				referenced = true
			}
		case "OpenAI":
			if provider.Spec.OpenAI != nil &&
				provider.Spec.OpenAI.APIKeySecretRef.Name == secret.GetName() {
				referenced = true
			}
		}

		if referenced {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      provider.Name,
					Namespace: provider.Namespace,
				},
			})
		}
	}
	return requests
}
