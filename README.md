<p align="center">
  <strong>
    <a href="/docs/README.md">Quickstart</a>&nbsp;&nbsp;&bull;&nbsp;&nbsp;
    <a href="/docs/administrators/README.md">Administrator docs</a>&nbsp;&nbsp;&bull;&nbsp;&nbsp;
    <a href="/docs/development/README.md">Development docs</a>&nbsp;&nbsp;&bull;&nbsp;&nbsp;
  </strong>
</p>

# Observability Operator

Observability Operator is a Kubernetes Operator that manages and
orchestrates the infrastructure to collect and relay Observability
data to several destinations in multi-teant Kubernetes clusters.

It aims to require minimal configuration and reduce the toil of a
feature team to get Observability data from their workloads by
providing sane defaults and automatically discovering as much
information as possible on behalf of the user.

It captures telemetry of tenant workloads (won't instrument the entire
cluster), can capture metrics, logs and traces and will relay the
telemetry to the tenant systems.

## Supported integrations

* Metrics: scrape Prometheus format from Pods
* Logs: any format, preferably JSON
* Traces: OpenTelemetry format
* Destination:
  * Metrics: any Prometheus RemoteWrite-compatible destination
  * Logs: any Fluentd supported format
  * Traces: any OpenTelemetry-compatible receiver

## Technologies

Observability Operator relies on the cluster already providing some
services. Cluster admins are expected to fulfill these needds in
advance:

* [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator)
* [Kube Fluentd Operator](https://github.com/vmware/kube-fluentd-operator)

For traces we use [Grafana
Alloy](https://grafana.com/docs/alloy/latest/) but we manage its
deployment directly in this operator, so there are no dependenciies
to install.

## Contributing

Refer to Please refer to the [code contribution guide](./CONTRIBUTING.md).

You can find further details of the operator in our [development documentation](./docs/development/README.md).

# License

This project is released under MIT license. You can find a copy of the
license terms in the LICENSE file.
