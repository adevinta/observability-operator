# Observability Operator for administrators

This documentation explains in detail how to operate Grafana Cloud
Operator in your cluster(s).

## Cluster Requirements

### Runtime Dependencies

Observability Operator can collect metrics, traces and logs. It
manages the entire requirements to gather traces, but has dependencies
for the other cases.

| Dependency              | Operator version                      | Dependency version                 |
| ----------------------- | ------------------------------------- | ---------------------------------- |
| `Prometheus Operator`   | > `v0.31.0` (verified with `v0.74.0`) | verified with Prometheus `v2.43.0` |
| `Kube Fluentd Operator` | verified with `v1.18.1`               | verified with fluentd `1.16.1`     |

#### `Prometheus Operator`

Observability Operator relies on the `monitoring.coreos.com/v1` resources
provided by `Prometheus Operator` CRDs. These resources were originally introduced
in version `v0.31.0` of the `Prometheus Operator`.

Observability Operator currently manages (through `Prometheus Operator`) Prometheus instances based
on version `v2.43.0`. This version of Prometheus might be customized in Observability Operator's helm values
via the `prometheusVersion` variable.

#### `Kube Fluentd Operator`

Observability Operator relies on `fluentd` to forward tenant logs to their corresponding storage backend. Tenants must annotate their namespace to specify which Grafana Cloud stack their logs should be sent to.

To enable this integration with Grafana Cloud, the Observability Operator dynamically updates a Configmap (named `grafana-cloud-fluentd-config`) with the required fluentd rules[^1].

`Kube Fluentd Operator` must load these rules from the `grafana-cloud-fluentd-config` configmap into `fluentd`s configuration, otherwise logs wont reach any of tenant stacks in Grafana Cloud.

This requires `Kube Fluentd Operator` configuration to follow these steps:

* define a volume in the `fluentd` pod that loads the Observability Operator's configmap and loki key/paths:

```yaml
      - name: loki-config
        configMap:
          name: grafana-cloud-fluentd-config
          optional: true
          items:
            - key: loki
              path: loki
```

* mount the contents of the previously defined volume as a `/templates/loki.conf` file in the `fluentd` pod:

```yaml
        - name: loki-config
          mountPath: /templates/loki.conf
          subPath: loki
```

* add the following directive into `fluentd`'s `fluent.conf` main configuration to load Observability Operator managed rules, stored in `grafana-cloud-fluentd-config` configmap:

```xml
  @include loki.conf
```

In addition to this, log events that shall be forwarded to Grafana Cloud must be tagged with a `loki.` prefix.

The following `fluentd` directives show how to achieve this:

> [!NOTE]
> This setup assumes logs are collected at the node level by `fluentbit` agents and forwarded to a `fluentd` service managed by `Kube Fluentd Operator`):

```xml
            <filter fluentbit.**>
              @type record_modifier
              remove_keys _dummy_
              <record>
                kubernetes_namespace_container_name ${record["kubernetes"]["namespace_name"]}.${record["kubernetes"]["pod_name"].split('.')[-1]}.${record["kubernetes"]["container_name"]}
                # dark magic to change nested attributes, this record is removed inmediately, it is only used to change kubernetes.pod_name value
                _dummy_ ${record['kubernetes']['pod_name'] = record['kubernetes']['pod_name'].split('.')[-1]; nil}
              </record>
            </filter>

            # retag based on the namespace and container name of the log message
            <match fluentbit.**>
              @type rewrite_tag_filter
              @id record_tag_rewrite
              # Update the tag have a structure of kube.<namespace>.<containername>
              <rule>
                key      kubernetes_namespace_container_name
                pattern  ^(.+)$
                tag      kube.$1
              </rule>
            </match>

            <match kube.**>
                @type rewrite_tag_filter
                @id copy_log_to_loki
                <rule>
                  key      $.kubernetes.container_name
                  pattern  /.+/
                  tag loki.${tag}
                </rule>
            </match>
```

The previous snippet will:

1. tag container logs (captured via fluentbit node agents) with `kube.` tag
2. prepend a `loki.` tag to the container logs that originated from namespaces with log collection enabled.

Users who are not using Grafana Cloud can send their logs to a different storage backend by creating the corresponding fluentd rules in a Configmap in their namespace. This Configmap must be named `fluentd-config`.

> [!WARNING]
> By default, `Kube Fluentd Operator` compiles all ConfigMaps named `fluentd-config` from every namespace to create the final Fluentd configuration. Any syntax error in these ConfigMaps will affect the entire `Fluentd` log processing pipeline.

A simplified example for sending logs to a custom fluentd backend follows:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
 name: fluentd-config
data:
 fluent.conf: |
   <match **>
    @type forward
    @id <TENANT_NAMESPACE>_match_forward
    <server>
      name forwarder-server
      host <FLUENTD_HOST>
      port <FLUENTD_PORT>
    </server>
    …
  </match>
```

### Networking

You have to provide some namespaces for the operator to allocate the
Prometheus storage systems and the OpenTelemetry collectors. We
recommend to use separate namespaces for the Prometheus and the
OpenTelemetry collectors as they have different networking access
requirements (pull-model vs push-model).

#### Networking requirements for Metrics

The namespace for the Prometheus systems should be allowed to access
all tenant namespaces, so it can scrape the workloads. This is
expected to be provisioned by the cluster owners.

#### Networking requirements for Traces

For traces, the operator provisions NetworkPolicy objects to grant the
required accesses between the namespaces holding the workloads and the
namespaces holding the collectors and Prometheus deployments.

This scheme assumes:

- your cluster has the components required to enforce these
 NetworkPolicy objects
- communication between namespace objects is blocked by default
  (allow-list pattern)

The NetworkPolicy objects created restrict access to the
OpentTelemetry collector's Service for a specific namespace to
workloads of that namespace, matching collectors and namespaces 1:1.

## Helm Chart

The Helm chart provides the following options:

| Option                              | Required? | Defaults                     | Description                                                                                                                                                                                     |
| ----------------------------------- | --------- | ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| replicaCount                        | Yes       | 1                            | How many operator replicas to deploy                                                                                                                                                            |
| image.registry                      | Yes       |                              | The registry hosting the operator image.                                                                                                                                                        |
| image.repository                    | Yes       |                              | The path to the operator image inside that registry.                                                                                                                                            |
| image.tag                           | Yes       |                              | The tag of the operator image to be deployed.                                                                                                                                                   |
| image.imagePullPolicy               | Yes       |                              | The operator image pull policty used by the Operator pod.                                                                                                                                       |
| region                              | No        | eu-west-1                    | Metadata added to telemetry data                                                                                                                                                                |
| clusterName                         | No        | CHANGEME                     | Metadata added to telemetry data                                                                                                                                                                |
| clusterDomain                       | Yes       | CHANGEME                     | The cluster domain. Used to generate the name of labels and annotations used to configure the operator behaviour as well as internal consistency.                                               |
| roleARN                             | Yes       | CHANGEME                     | The AWS ARN for the IAM role the Operator should adopt. This is required to grant interaction with the AWS API.                                                                                 |
| exclusionLabelSelectors.workload    | No        |                              | List of workload label selectors that should be ignored. Deployments of this name will be skipped despite how they are configured.                                                              |
| exclusionLabelSelectors.namespace   | No        |                              | List of namespace label selectors that should be ignored. The telemetry of workloads present in the listed namespaces will be ignored no matter how they are configured.                        |
| enableGrafanaCloud                  | Yes       | false                        | Whether to set up the discovered GrafanaCloud stacks as destination for telemetry. If disabled, fallbacks have to be setup on each individual namespace for each telemetry type.                |
| enableVPA                           | No        | false                        | Whether to allocate VPA objects for the Prometheus and Alloy deployments that collect tenant telemetry.                                                                                         |
| prometheusDockerImage.tag           | Yes       | v2.43.0                      | Tag of the Prometheus image to be used when provisioning Prometheus statefulsets for tenant metrics scraping and storage.                                                                       |
| service.internalPort                | Yes       | 8080                         | Internal port for exposing Operator metrics in prometheus format                                                                                                                                |
| enableSelfVpa                       | No        | true                         | Create a VPA object for the operator itself                                                                                                                                                     |
| resources.limits.cpu                | No        | 100m                         | The CPU limits of the operator deployment                                                                                                                                                       |
| resources.limits.memory             | No        | 128Mi                        | The memory limits of the operator deployment                                                                                                                                                    |
| resources.requests.cpu              | No        | 100m                         | The CPU requests of the operator deployment                                                                                                                                                     |
| resources.requests.memory           | No        | 128Mi                        | The memory requests of the operator deployment                                                                                                                                                  |
| grafanacloud.configmap.namespace    | Yes       |                              | The namespace holding the configmap with Loki credentials to send logs to GrafanaCloud. The credentials are the same for all tenants in the cluster.                                            |
| grafanacloud.configmap.name         | Yes       |                              | The name of the configmap with Loki credentials to send logs to GrafanaCloud. The credentials are the same for all tenants in the cluster.                                                      |
| grafanacloud.configmap.lokikey      | Yes       |                              | The configmap key holding Loki credentials to send logs to GrafanaCloud. The credentials are the same for all tenants in the cluster.                                                           |
| namespaces.tracesNamespace.name     | Yes       | observability                | The name of the provisioned namespace where Alloy deployments will be allocated to                                                                                                              |
| namespaces.prometheusNamespace.name | Yes       | platform-services            | The name of the provisioned namespace where Prometheus deployments will be allocated to                                                                                                              |

[^1]: <https://github.com/adevinta/observability-operator/blob/master/pkg/grafanacloud/fluentd_template.conf>
