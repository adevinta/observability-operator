package grafanacloud

import (
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type GrafanaStackReconciler struct {
	Log                      logr.Logger
	GrafanaCloudClient       GrafanaCloudStackLister
	GrafanaStackChangeEvents chan event.GenericEvent
	OriginalStacks           map[string]struct{}
}

func (r *GrafanaStackReconciler) WatchGrafanaStacksChange(tick <-chan time.Time) error {

	stacks, err := r.listStacks()

	if err != nil {
		r.Log.Error(err, "Failed to list grafana cloud stacks")
		return err
	}

	r.OriginalStacks = stacks

	go func() {
		for range tick {
			err := r.reconcile()
			if err != nil {
				r.Log.Error(err, "Failed to list grafana cloud stacks, it will be retried.")
			}
		}
	}()
	return nil
}

func (r *GrafanaStackReconciler) reconcile() error {
	updatedStacks, err := r.listStacks()
	if err != nil {
		return err
	}
	for updatedStack := range updatedStacks {
		if _, found := r.OriginalStacks[updatedStack]; !found {
			r.Log.Info("new stack is found", "stack-name", updatedStack)
			r.GrafanaStackChangeEvents <- event.GenericEvent{
				Object: &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: updatedStack,
					},
				},
			}
		}
	}
	for originalStack := range r.OriginalStacks {
		if _, found := updatedStacks[originalStack]; !found {
			r.Log.Info("stack is deleted", "stack-name", originalStack)
			r.GrafanaStackChangeEvents <- event.GenericEvent{
				Object: &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: originalStack,
					},
				},
			}
		}
	}
	r.OriginalStacks = updatedStacks
	return nil
}

func (r *GrafanaStackReconciler) listStacks() (map[string]struct{}, error) {
	stacks, err := r.GrafanaCloudClient.ListStacks()
	if err != nil {
		r.Log.Error(err, "Failed to list grafana cloud stacks")
		return nil, err
	}
	out := map[string]struct{}{}
	for _, stack := range stacks {
		out[stack.Slug] = struct{}{}
	}
	return out, nil
}
