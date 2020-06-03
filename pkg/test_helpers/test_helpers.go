package test_helpers

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/require"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

func NewFakeClient(t *testing.T, initialObjects ...runtime.Object) client.Client {
	s := runtime.NewScheme()
	for gvk := range scheme.Scheme.AllKnownTypes() {
		obj, err := scheme.Scheme.New(gvk)
		require.NoError(t, err)
		s.AddKnownTypes(gvk.GroupVersion(), obj)
	}
	require.NoError(t, monitoringv1.AddToScheme(s))
	require.NoError(t, vpav1.AddToScheme(s))
	return fake.NewClientBuilder().WithRuntimeObjects(initialObjects...).WithScheme(s).Build()
}
