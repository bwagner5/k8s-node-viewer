package kube

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oxidecomputer/k8s-node-viewer/internal/model"
)

// pollMetrics samples metrics.k8s.io on a timer.
//
// Polling, not watching: the metrics API is an aggregated API server with no
// watch support, and its data is a rolling window anyway. Two list calls every
// few seconds is far cheaper than it looks — the payload is tiny compared to the
// pod cache we already hold.
//
// A failed poll is intentionally silent. metrics-server restarting mid-demo
// should leave the last known values on screen, not blank the meters or throw an
// error banner.
func (s *Source) pollMetrics(ctx context.Context) {
	ticker := time.NewTicker(s.opts.MetricsRate)
	defer ticker.Stop()
	for {
		s.sampleNodeMetrics(ctx)
		s.samplePodMetrics(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Source) sampleNodeMetrics(ctx context.Context) {
	list, err := s.clients.metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for i := range list.Items {
		m := &list.Items[i]
		s.store.SetNodeUsage(m.Name, usageResources(m.Usage[corev1.ResourceCPU], m.Usage[corev1.ResourceMemory]))
	}
}

func (s *Source) samplePodMetrics(ctx context.Context) {
	list, err := s.clients.metrics.MetricsV1beta1().PodMetricses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for i := range list.Items {
		m := &list.Items[i]
		var total model.Resources
		for j := range m.Containers {
			c := &m.Containers[j]
			total = total.Add(usageResources(c.Usage[corev1.ResourceCPU], c.Usage[corev1.ResourceMemory]))
		}
		s.store.SetPodUsage(m.Namespace+"/"+m.Name, total)
	}
}
