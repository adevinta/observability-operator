package grafanacloud

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type MockStackListerFunc struct {
	ListStacksCalls int32
	ListStacksFunc  func() (Stacks, error)
}

func (cg *MockStackListerFunc) ListStacks() (Stacks, error) {
	atomic.AddInt32(&cg.ListStacksCalls, 1)
	if cg.ListStacksFunc != nil && cg.ListStacksCalls == 1 {
		return cg.ListStacksFunc()
	}
	return nil, errors.New("ListStacks not implemented")
}
func TestWatchPeriodicallySyncsGrafanaCloudStacks(t *testing.T) {
	grafanaStackChangeEvents := make(chan event.GenericEvent)
	gcClient := &MockStackListerFunc{
		ListStacksFunc: func() (Stacks, error) {
			fmt.Println("listStacks")
			return []Stack{
				{Slug: "adevintaruntime", LogsInstanceID: 9876, LogsURL: "https://logs.grafanacloud.es"},
			}, nil
		},
	}
	reconciler := &GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
		OriginalStacks:           map[string]struct{}{},
	}
	c := make(chan time.Time)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		assert.NoError(t, reconciler.WatchGrafanaStacksChange(c))
		wg.Done()
	}()
	c <- time.Now()
	close(c)
	wg.Wait()
	assert.EqualValues(t, 2, gcClient.ListStacksCalls)
}
func TestWatchGrafanaStacksChangeFailsHardWhenInitialListFails(t *testing.T) {
	grafanaStackChangeEvents := make(chan event.GenericEvent, 10)
	gcClient := &MockStackListerFunc{
		ListStacksFunc: func() (Stacks, error) {
			return nil, errors.New("test-error")
		},
	}
	reconciler := &GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
		OriginalStacks:           map[string]struct{}{},
	}
	c := make(chan time.Time)
	close(c)
	assert.Error(t, reconciler.WatchGrafanaStacksChange(c))
}
func TestGrafanaStackReconcileReturnsAnErrorWhenClientListFails(t *testing.T) {
	grafanaStackChangeEvents := make(chan event.GenericEvent)
	gcClient := &MockStackListerFunc{
		ListStacksFunc: func() (Stacks, error) {
			return nil, errors.New("test-error")
		},
	}
	reconciler := &GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
		OriginalStacks:           map[string]struct{}{},
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for evt := range grafanaStackChangeEvents {
			switch evt.Object.GetName() {
			default:
				assert.Fail(t, "should trigger a reconciliation of grafanacloud stack %s", evt.Object.GetName())
			}
		}
		wg.Done()
	}()
	assert.Error(t, reconciler.reconcile())
	assert.EqualValues(t, 1, gcClient.ListStacksCalls)
	close(grafanaStackChangeEvents)
	assert.Equal(t, map[string]struct{}{}, reconciler.OriginalStacks)
	wg.Wait()
}
func TestGrafanaStackReconcileTriggersUpdateForAddedAndDeletedStacks(t *testing.T) {
	grafanaStackChangeEvents := make(chan event.GenericEvent)
	gcClient := &MockStackListerFunc{
		ListStacksFunc: func() (Stacks, error) {
			return []Stack{
				{Slug: "adevintatenant", LogsInstanceID: 1234, LogsURL: "https://logs.grafanacloud.de"},
				{Slug: "adevintaruntime", LogsInstanceID: 9876, LogsURL: "https://logs.grafanacloud.es"},
				{Slug: "adevintanewtenants1", LogsInstanceID: 9878, LogsURL: "https://logs.grafanacloud.fr"},
			}, nil
		},
	}
	reconciler := &GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
		OriginalStacks: map[string]struct{}{
			"adevintatenant":      {},
			"adevintaothertenant": {},
			"adevintaruntime":     {},
		},
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for evt := range grafanaStackChangeEvents {
			switch evt.Object.GetName() {
			case "adevintanewtenants1":
			case "adevintaothertenant":
			default:
				assert.Fail(t, "unexpected reconciliation of grafanacloud stack %s", evt.Object.GetName())
			}
		}
		wg.Done()
	}()
	require.NoError(t, reconciler.reconcile())
	assert.EqualValues(t, 1, gcClient.ListStacksCalls)
	close(grafanaStackChangeEvents)
	assert.Equal(t, map[string]struct{}{
		"adevintatenant":      {},
		"adevintanewtenants1": {},
		"adevintaruntime":     {},
	}, reconciler.OriginalStacks)
	wg.Wait()
}
func TestGrafanaStackReconcileStackNoChangesDoesNotPopulateEvents(t *testing.T) {
	grafanaStackChangeEvents := make(chan event.GenericEvent)
	gcClient := &MockStackListerFunc{
		ListStacksFunc: func() (Stacks, error) {
			return []Stack{
				{Slug: "adevintatenant", LogsInstanceID: 1234, LogsURL: "https://logs.grafanacloud.de"},
				{Slug: "adevintaruntime", LogsInstanceID: 9876, LogsURL: "https://logs.grafanacloud.es"},
				{Slug: "adevintaothertenant", LogsInstanceID: 9878, LogsURL: "https://logs.grafanacloud.fr"},
			}, nil
		},
	}
	reconciler := &GrafanaStackReconciler{
		Log:                      ctrl.Log.WithName("controllers").WithName("GrafanaStack"),
		GrafanaCloudClient:       gcClient,
		GrafanaStackChangeEvents: grafanaStackChangeEvents,
		OriginalStacks: map[string]struct{}{
			"adevintatenant":      {},
			"adevintaothertenant": {},
			"adevintaruntime":     {},
		},
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for evt := range grafanaStackChangeEvents {
			switch evt.Object.GetName() {
			default:
				assert.Fail(t, "unexpected reconciliation of grafanacloud stack %s", evt.Object.GetName())
			}
		}
		wg.Done()
	}()
	require.NoError(t, reconciler.reconcile())
	assert.EqualValues(t, 1, gcClient.ListStacksCalls)
	close(grafanaStackChangeEvents)
	assert.Equal(t, reconciler.OriginalStacks, map[string]struct{}{
		"adevintatenant":      {},
		"adevintaothertenant": {},
		"adevintaruntime":     {},
	})
	wg.Wait()
}
