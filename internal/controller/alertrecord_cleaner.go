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
	"time"

	smartlogv1alpha1 "github.com/gitlayzer/smart-log/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type AlertRecordCleaner struct {
	client.Client
	Interval time.Duration
}

// +kubebuilder:rbac:groups=smartlog.smart-tools.com,resources=alertrecords,verbs=list;delete;watch

func (c *AlertRecordCleaner) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("AlertRecordCleaner")
	logger.Info("Starting cleaner", "interval", c.Interval.String())
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Stopping cleaner")
			return nil
		case <-ticker.C:
			logger.Info("Running cleanup cycle for AlertRecords")
			c.cleanup(ctx)
		}
	}
}

func (c *AlertRecordCleaner) cleanup(ctx context.Context) {
	logger := log.FromContext(ctx)
	var records smartlogv1alpha1.AlertRecordList

	if err := c.List(ctx, &records); err != nil {
		logger.Error(err, "Failed to list AlertRecords for cleanup")
		return
	}

	for _, record := range records.Items {
		ttlAnnotation, ok := record.Annotations["smartlog.smart-tools.com/ttl"]
		if !ok {
			continue
		}

		ttl, err := time.ParseDuration(ttlAnnotation)
		if err != nil {
			logger.Error(err, "Failed to parse TTL annotation", "record", record.Name, "ttl", ttlAnnotation)
			continue
		}

		expirationTime := record.CreationTimestamp.Add(ttl)
		if time.Now().After(expirationTime) {
			logger.Info("Deleting expired AlertRecord", "record", record.Name, "expirationTime", expirationTime)
			if err := c.Delete(ctx, &record); err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "Failed to delete expired AlertRecord", "record", record.Name)
			}
		}
	}
}
