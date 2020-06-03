package grafanacloud

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type GrafanaCloudClientFunc struct {
	ListStacksCalls int
	ListStacksFunc  func() (Stacks, error)
}

func (cg *GrafanaCloudClientFunc) ListStacks() (Stacks, error) {
	cg.ListStacksCalls++
	if cg.ListStacksFunc != nil {
		return cg.ListStacksFunc()
	}
	return nil, errors.New("ListStacks not implemented")
}

// Allow to reduce the number functions to write by composing with the interface.
type testK8sLister struct {
	client.Client
	ListCalls   int
	ListFunc    func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	GetCalls    int
	GetFunc     func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	UpdateCalls int
	UpdateFunc  func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
	CreateCalls int
	CreateFunc  func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
}

func (t *testK8sLister) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	t.ListCalls++
	if t.ListFunc != nil {
		return t.ListFunc(ctx, list, opts...)
	}
	if t.Client != nil {
		return t.Client.List(ctx, list, opts...)
	}
	return errors.New("List not implemented")
}

func (t *testK8sLister) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	t.GetCalls++
	if t.GetFunc != nil {
		return t.GetFunc(ctx, key, obj, opts...)
	}
	if t.Client != nil {
		return t.Client.Get(ctx, key, obj, opts...)
	}
	return errors.New("Get not implemented")
}

func (t *testK8sLister) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	t.UpdateCalls++
	if t.UpdateFunc != nil {
		return t.UpdateFunc(ctx, obj, opts...)
	}
	if t.Client != nil {
		return t.Client.Update(ctx, obj, opts...)
	}
	return errors.New("Update not implemented")
}

func (t *testK8sLister) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	t.CreateCalls++
	if t.CreateFunc != nil {
		return t.CreateFunc(ctx, obj, opts...)
	}
	if t.Client != nil {
		return t.Client.Create(ctx, obj, opts...)
	}
	return errors.New("Createnot implemented")
}

func TestFluentdConfigTemplateCorrectness(t *testing.T) {
	// This test verifies that changes to the template result in valid, executable templates
	tpl := template.New("fluentd-config")
	_, err := tpl.Parse(lokiFluentDTemplate)
	require.NoError(t, err, "couldn't parse Fluentd configuration template successfully")

	t.Run("when there are no namespaces, fluentd config file generated is syntactically correct", func(t *testing.T) {
		config := lokiOptions{
			Cluster: clusterDetails{
				Name:   "my-cluster",
				Region: "eu-fake-1",
			},
			Stacks: map[string][]lokiCredentials{},
		}
		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Fluentd configuration template successfully")
	})
	t.Run("when a namespace has a single stack, fluentd config file generated is syntactically correct", func(t *testing.T) {
		namespace := "my-namespace"
		config := lokiOptions{
			Cluster: clusterDetails{
				Name:   "my-cluster",
				Region: "eu-fake-1",
			},
			Stacks: map[string][]lokiCredentials{
				namespace: {
					{
						URL:    "http://grafana.net/loki",
						UserID: 123,
					},
				},
			},
		}

		expectedFluentdconf := `<filter loki.kube.*-dev.**>
  @type record_modifier
  @id inject_loki_user_environment_dev
  <record>
    environment "dev"
  </record>
</filter>

<filter loki.kube.*-pre.**>
  @type record_modifier
  @id inject_loki_user_environment_pre
  <record>
    environment "pre"
  </record>
</filter>

<filter loki.kube.*-pro.**>
  @type record_modifier
  @id inject_loki_user_environment_pro
  <record>
    environment "pro"
  </record>
</filter>
<match kube.my-namespace**.access-log>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_access_log_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 1m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      namespace
      ingress_class
      ingress_name
      method
    </label>
  </store>
</match>

<match loki.kube.my-namespace**>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 64m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      container $.kubernetes.container_name
      namespace $.kubernetes.namespace_name
      pod $.kubernetes.pod_name
      app $.kubernetes.labels.app
    </label>
  </store>
</match>
`

		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Fluentd configuration template successfully")
		require.Equal(t, expectedFluentdconf, buffer.String())
	})
	t.Run("when a namespace has multiple stacks, fluentd config file generated is syntactically correct", func(t *testing.T) {
		config := lokiOptions{
			Cluster: clusterDetails{
				Name:   "my-cluster",
				Region: "eu-fake-1",
			},
			Stacks: map[string][]lokiCredentials{
				"my-namespace": {
					{
						URL:    "http://grafana.net/loki",
						UserID: 123,
					},
					{
						URL:    "http://grafana.net/loki",
						UserID: 456,
					},
				},
			},
		}

		expectedFluentdconf := `<filter loki.kube.*-dev.**>
  @type record_modifier
  @id inject_loki_user_environment_dev
  <record>
    environment "dev"
  </record>
</filter>

<filter loki.kube.*-pre.**>
  @type record_modifier
  @id inject_loki_user_environment_pre
  <record>
    environment "pre"
  </record>
</filter>

<filter loki.kube.*-pro.**>
  @type record_modifier
  @id inject_loki_user_environment_pro
  <record>
    environment "pro"
  </record>
</filter>
<match kube.my-namespace**.access-log>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_access_log_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 1m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      namespace
      ingress_class
      ingress_name
      method
    </label>
  </store>
  <store>
    @type loki
    @id loki_loki_my-namespace_access_log_1
    url "http://grafana.net/loki"
    username "456"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 1m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      namespace
      ingress_class
      ingress_name
      method
    </label>
  </store>
</match>

<match loki.kube.my-namespace**>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 64m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      container $.kubernetes.container_name
      namespace $.kubernetes.namespace_name
      pod $.kubernetes.pod_name
      app $.kubernetes.labels.app
    </label>
  </store>
  <store>
    @type loki
    @id loki_loki_my-namespace_1
    url "http://grafana.net/loki"
    username "456"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 64m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      container $.kubernetes.container_name
      namespace $.kubernetes.namespace_name
      pod $.kubernetes.pod_name
      app $.kubernetes.labels.app
    </label>
  </store>
</match>
`

		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Fluentd configuration template successfully")
		require.Equal(t, expectedFluentdconf, buffer.String())
	})
	t.Run("when we have several namespaces, fluentd config file generated is syntactically correct", func(t *testing.T) {
		config := lokiOptions{
			Cluster: clusterDetails{
				Name:   "my-cluster",
				Region: "eu-fake-1",
			},
			Stacks: map[string][]lokiCredentials{
				"my-namespace": {
					{
						URL:    "http://grafana.net/loki",
						UserID: 123,
					},
				},
				"other-namespace": []lokiCredentials{
					{
						URL:    "http://grafana.net/loki",
						UserID: 456,
					},
				},
			},
		}

		expectedFluentdconf := `<filter loki.kube.*-dev.**>
  @type record_modifier
  @id inject_loki_user_environment_dev
  <record>
    environment "dev"
  </record>
</filter>

<filter loki.kube.*-pre.**>
  @type record_modifier
  @id inject_loki_user_environment_pre
  <record>
    environment "pre"
  </record>
</filter>

<filter loki.kube.*-pro.**>
  @type record_modifier
  @id inject_loki_user_environment_pro
  <record>
    environment "pro"
  </record>
</filter>
<match kube.my-namespace**.access-log>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_access_log_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 1m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      namespace
      ingress_class
      ingress_name
      method
    </label>
  </store>
</match>

<match loki.kube.my-namespace**>
  @type copy
  <store>
    @type loki
    @id loki_loki_my-namespace_0
    url "http://grafana.net/loki"
    username "123"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 64m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      container $.kubernetes.container_name
      namespace $.kubernetes.namespace_name
      pod $.kubernetes.pod_name
      app $.kubernetes.labels.app
    </label>
  </store>
</match>
<match kube.other-namespace**.access-log>
  @type copy
  <store>
    @type loki
    @id loki_loki_other-namespace_access_log_0
    url "http://grafana.net/loki"
    username "456"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 1m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      namespace
      ingress_class
      ingress_name
      method
    </label>
  </store>
</match>

<match loki.kube.other-namespace**>
  @type copy
  <store>
    @type loki
    @id loki_loki_other-namespace_0
    url "http://grafana.net/loki"
    username "456"
    password "#{ENV['LOKI_PASSWORD']}"
    buffer_chunk_limit 64m
    <buffer>
      flush_interval 60s # default
      flush_at_shutdown true # default
      retry_timeout 60s # instead of the default 72h
      retry_max_times 5 # instead of the default 17 (i.e. 65536 seconds, or 1092 minutes or 18 hours)
      retry_max_interval 10
      flush_thread_count 1 # default
      overflow_action drop_oldest_chunk
      disable_chunk_backup true
    </buffer>
    # this instructs loki to send each "fluentd record" as JSON
    # i.e. '{"log": "${lineLoggedByUser}", "time": "2021-01-01T00:00", "kubernetes": {"namespace": {"name": "${namespaceName}"}}}' <etc>
    # removing this line transforms the record in kubernetes={"namespace": {"name": "${namespaceName}"}}} time=2021-01-01T00:00 log=${lineLoggedByUser}
    # that is unparsable on the loki query side
    line_format json
    extra_labels {"cluster": "my-cluster","region":"eu-fake-1"}
    remove_keys _sumo_metadata,source,category,host
    <label>
      environment
      container $.kubernetes.container_name
      namespace $.kubernetes.namespace_name
      pod $.kubernetes.pod_name
      app $.kubernetes.labels.app
    </label>
  </store>
</match>
`

		var buffer bytes.Buffer
		err = tpl.Execute(&buffer, config)
		require.NoError(t, err, "couldn't execute Fluentd configuration template successfully")
		require.Equal(t, expectedFluentdconf, buffer.String())
	})
}

func TestRateLimiterFunc(t *testing.T) {
	calls := 0
	f := RateLimitedFunc{
		Do: func(context.Context) {
			calls += 1
		},
		Queue: workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](1*time.Millisecond, 100*time.Millisecond)),
	}
	go f.Start()
	for i := 0; i < 100; i++ {
		f.EnsureDone()
	}
	time.Sleep(400 * time.Millisecond)
	assert.InDelta(t, 3, calls, 4)
}

func TestGrafanaCloudCreateLokiConfigMaps(t *testing.T) {
	k8sClient := testK8sLister{
		Client: fake.NewClientBuilder().WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-dev", Labels: map[string]string{Config.logsLabelKey: "disabled"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-tenant-pro", Annotations: map[string]string{Config.stackNameAnnotationKey: "adevintaothertenant"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "another-tenant-pro", Labels: map[string]string{Config.logsLabelKey: "enabled"}, Annotations: map[string]string{Config.stackNameAnnotationKey: "adevintaanothertenant"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-without-env-in-name", Annotations: map[string]string{Config.stackNameAnnotationKey: "adevintatenant"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-not-in-gc-pro"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "system",
				Annotations: map[string]string{
					Config.stackNameAnnotationKey: "adevintaruntime",
				},
			}},
		).Build(),
		CreateFunc: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				assert.Equal(t, "test-platform-services", cm.Namespace)
				assert.Equal(t, "test-fluentd-config", cm.Name)
				require.Contains(t, cm.Data, "loki")
				assert.NotContains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.tenant-dev**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.other-tenant-pro**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "5678"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.another-tenant-pro**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_another-tenant-pro_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "6789"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.tenant-without-env-in-name**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-without-env-in-name_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.NotContains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.tenant-not-in-gc-pro**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.system**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_system_0`,
					`    url "https://logs.grafanacloud.es"`,
					`    username "9876"`,
				},
					"\n",
				))
				// ingress access logs
				assert.NotContains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.tenant-dev**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_access_log_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.other-tenant-pro**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_access_log_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "5678"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.another-tenant-pro**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_another-tenant-pro_access_log_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "6789"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.tenant-without-env-in-name**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-without-env-in-name_access_log_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.NotContains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.tenant-not-in-gc-pro**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_access_log_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.system**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_system_access_log_0`,
					`    url "https://logs.grafanacloud.es"`,
					`    username "9876"`,
				},
					"\n",
				))
				assert.NotContains(t, cm.Data["loki"], "tenant-not-in-gc-pro")
				return nil
			}
			t.Errorf("unexpected object create %T", obj)
			return errors.New("unexpected object")
		},
	}
	gcClient := &GrafanaCloudClientFunc{
		ListStacksFunc: func() (Stacks, error) {
			return []Stack{
				{Slug: "adevintatenant", LogsInstanceID: 1234, LogsURL: "https://logs.grafanacloud.de"},
				{Slug: "adevintaothertenant", LogsInstanceID: 5678, LogsURL: "https://logs.grafanacloud.com"},
				{Slug: "adevintaanothertenant", LogsInstanceID: 6789, LogsURL: "https://logs.grafanacloud.com"},
				{Slug: "adevintaruntime", LogsInstanceID: 9876, LogsURL: "https://logs.grafanacloud.es"},
			}, nil
		},
	}
	updater := &GrafanaCloudConfigUpdater{
		Client:             &k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("Namespace"),
		GrafanaCloudClient: gcClient,
		ClusterName:        "test-cluster",
		ClusterRegion:      "adv-bcn-1",
		ConfigMapNamespace: "test-platform-services",
		ConfigMapName:      "test-fluentd-config",
		ConfigMapLokiKey:   "loki",
	}
	go updater.Start(workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Microsecond, 1*time.Millisecond)))
	time.Sleep(500 * time.Microsecond)
	for i := 0; i < 100; i++ {
		err := updater.InjectFluentdLokiConfiguration(context.Background())
		require.NoError(t, err)
	}
	time.Sleep(10 * time.Millisecond)
	assert.LessOrEqual(t, k8sClient.GetCalls, 20)
	assert.LessOrEqual(t, k8sClient.CreateCalls, 20)
	assert.GreaterOrEqual(t, k8sClient.GetCalls, 1)
	assert.GreaterOrEqual(t, k8sClient.CreateCalls, 1)
}

func TestGrafanaCloudCreateLokiConfigMapsForMultipleStacks(t *testing.T) {
	k8sClient := testK8sLister{
		Client: fake.NewClientBuilder().WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-dev", Annotations: map[string]string{Config.stackNameAnnotationKey: "adevintatenant"}}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "other-tenant-pro",
				Annotations: map[string]string{
					Config.stackNameAnnotationKey: "adevintastack1,adevintastack2",
				},
			}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-not-in-gc-pro"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: "system",
				Annotations: map[string]string{
					Config.stackNameAnnotationKey: "adevintaruntime",
				},
			}},
		).Build(),
		CreateFunc: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				assert.Equal(t, "test-platform-services", cm.Namespace)
				assert.Equal(t, "test-fluentd-config", cm.Name)
				require.Contains(t, cm.Data, "loki")
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.tenant-dev**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.other-tenant-pro**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "5566"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_1`,
					`    url "https://logs.grafanacloud.fr"`,
					`    username "6655"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match loki.kube.system**>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_system_0`,
					`    url "https://logs.grafanacloud.es"`,
					`    username "9876"`,
				},
					"\n",
				))
				// ingress access logs
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.tenant-dev**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_tenant-dev_access_log_0`,
					`    url "https://logs.grafanacloud.de"`,
					`    username "1234"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.other-tenant-pro**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_access_log_0`,
					`    url "https://logs.grafanacloud.com"`,
					`    username "5566"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_other-tenant-pro_access_log_1`,
					`    url "https://logs.grafanacloud.fr"`,
					`    username "6655"`,
				},
					"\n",
				))
				assert.Contains(t, cm.Data["loki"], strings.Join([]string{
					`<match kube.system**.access-log>`,
					`  @type copy`,
					`  <store>`,
					`    @type loki`,
					`    @id loki_loki_system_access_log_0`,
					`    url "https://logs.grafanacloud.es"`,
					`    username "9876"`,
				},
					"\n",
				))
				assert.NotContains(t, cm.Data["loki"], "tenant-not-in-gc-pro")
				return nil
			}
			t.Errorf("unexpected object create %T", obj)
			return errors.New("unexpected object")
		},
	}
	gcClient := &GrafanaCloudClientFunc{
		ListStacksFunc: func() (Stacks, error) {
			return []Stack{
				{Slug: "adevintatenant", LogsInstanceID: 1234, LogsURL: "https://logs.grafanacloud.de"},
				{Slug: "adevintaothertenant", LogsInstanceID: 5678, LogsURL: "https://logs.grafanacloud.com"},
				{Slug: "adevintaruntime", LogsInstanceID: 9876, LogsURL: "https://logs.grafanacloud.es"},
				{Slug: "adevintastack1", LogsInstanceID: 5566, LogsURL: "https://logs.grafanacloud.com"},
				{Slug: "adevintastack2", LogsInstanceID: 6655, LogsURL: "https://logs.grafanacloud.fr"},
			}, nil
		},
	}
	updater := &GrafanaCloudConfigUpdater{
		Client:             &k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("Namespace"),
		GrafanaCloudClient: gcClient,
		ClusterName:        "test-cluster",
		ClusterRegion:      "adv-bcn-1",
		ConfigMapNamespace: "test-platform-services",
		ConfigMapName:      "test-fluentd-config",
		ConfigMapLokiKey:   "loki",
	}
	go updater.Start(workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Microsecond, 1*time.Millisecond)))
	time.Sleep(500 * time.Microsecond)
	for i := 0; i < 100; i++ {
		err := updater.InjectFluentdLokiConfiguration(context.Background())
		require.NoError(t, err)
	}
	time.Sleep(10 * time.Millisecond)
	assert.LessOrEqual(t, k8sClient.GetCalls, 20)
	assert.LessOrEqual(t, k8sClient.CreateCalls, 20)
	assert.GreaterOrEqual(t, k8sClient.GetCalls, 1)
	assert.GreaterOrEqual(t, k8sClient.CreateCalls, 1)
}

func TestGrafanaCloudEmptyListDoesNotUpdateConfig(t *testing.T) {
	k8sClient := testK8sLister{
		GetFunc: func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return k8serrors.NewNotFound(schema.GroupResource{}, "resource")
		},
		ListFunc: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			if namespaces, ok := list.(*corev1.NamespaceList); ok {
				namespaces.Items = []corev1.Namespace{
					{ObjectMeta: metav1.ObjectMeta{Name: "tenant-dev"}},
				}
				return nil
			}
			t.Errorf("unexpected list object %T", list)
			return errors.New("unexpected object")
		},
		CreateFunc: func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				assert.Equal(t, "test-platform-services", cm.Namespace)
				assert.Equal(t, "test-fluentd-config", cm.Name)
				return nil
			}
			t.Errorf("unexpected object create %T", obj)
			return errors.New("unexpected object")
		},
	}
	gcClient := &GrafanaCloudClientFunc{
		ListStacksFunc: func() (Stacks, error) {
			return []Stack{}, nil
		},
	}
	updater := &GrafanaCloudConfigUpdater{
		Client:             &k8sClient,
		Log:                ctrl.Log.WithName("controllers").WithName("Namespace"),
		GrafanaCloudClient: gcClient,
		ClusterName:        "test-cluster",
		ClusterRegion:      "adv-bcn-1",
		ConfigMapNamespace: "test-platform-services",
		ConfigMapName:      "test-fluentd-config",
		ConfigMapLokiKey:   "loki",
	}
	go updater.Start(workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](10*time.Microsecond, 1*time.Millisecond)))
	time.Sleep(500 * time.Microsecond)
	for i := 0; i < 100; i++ {
		err := updater.InjectFluentdLokiConfiguration(context.Background())
		require.NoError(t, err)
	}
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 0, k8sClient.CreateCalls)
}
