# Send Pod traces to Grafana Cloud

> [!NOTE]
> Administrators can modify the domain of the cluster, which will affect the domain used by label and annotation keys.
> If the cluster's domain is `example.com` the labels and annotations will match it, e.g. `grafanacloud.example.com/stack-name`.
>
> In this document we'll use `adevinta.com` as the cluster domain.

## Overview

To start using traces, users need to:

1) setup the namespaces where traces are to be enabled

2) instrument the application(s) to send traces to the corresponding OTEL collector.

> [!NOTE]
> Currently, the tracing integration only supports **Grafana Cloud Tempo** as backend for traces.

## Configuring your namespaces

Your namespaces must have the `grafanacloud.adevinta.com/stack-name` annotation which specifies the Grafana Cloud
stack that your namespaces will use for sending metrics, logs and traces.

In addition to the `grafanacloud.adevinta.com/stack-name`, add the `grafanacloud.adevinta.com/traces` annotation to enable tracing:

```yaml
metadata:
  annotations:
    grafanacloud.adevinta.com/stack-name: "{my-grafana-cloud-stack-name}"
    grafanacloud.adevinta.com/traces: "enabled"
```

This will setup a dedicated OTel Collector for each configured namespace.

Collectors will be accessible from the user specified namespaces via the following endpoint:

* `otelcol-{namespace}.observability.svc.cluster.local`

and through ports:

* `4317` (for OTLP/gRPC connections)
* `4318` (for OTLP/HTTP connections)

## Instrumenting your apps

Once you have your [namespace ready](#Configuring-your-namespaces), you need to setup intrumentation in your apps to send traces to
your OTel Collector(s).

Users may configure the OTLP endpoint and protocol either through the OTel SDK and languange of their choice[^3] or via
environment variables[^4].

An example follows using environment variables:

* using OTLP/gRPC

  ```yaml
  # Deployment manifest
  spec:
    containers:
    - name: my-instrumented-app
      image: hello-app:1.0
      env:
      - name: OTEL_EXPORTER_OTLP_PROTOCOL
        value: "grpc"
      - name: OTEL_EXPORTER_OTLP_ENDPOINT
        value: "http://otelcol-{namespace}.observability.svc.cluster.local:4317"
  ```

* using OTLP/HTTP

  ```yaml
  # Deployment manifest
  spec:
    containers:
    - name: my-instrumented-app
      image: hello-app:1.0
      env:
      - name: OTEL_EXPORTER_OTLP_PROTOCOL
        value: "http/protobuf"
      - name: OTEL_EXPORTER_OTLP_ENDPOINT
        value: "http://otelcol-{namespace}.observability.svc.cluster.local:4318"
  ```

> [!WARNING]
> If you set the OTLP endpoint in your application code through the Otel SDK (instead of using environment variables) you may
> need to append the `/v1/traces` path. For example in Python[^5] this might look like this:
>
> ```python
>  from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
>
>  otlp_http_exporter = OTLPSpanExporter(
>     endpoint="http://otelcol-{namespace}.observability.svc.cluster.local:4318/v1/traces"
>  )
> ```
>
> Check the corresponding SDK documentation[^3] for more details.

## Disabling traces

You can delete your dedicated OTel Collector at any time by annotating your namespaces as follows:

```yaml
metadata:
  annotations:
    grafanacloud.adevinta.com//traces: "disabled"
```

Or simply removing the whole `grafanacloud.adevinta.com/traces` annotation.

Before deleting your OTel Collector make sure your app instrumenation is no longer sending traces to the collector, otherwise your
app will error out when trying to send spans.

## Sampling

The Operator provides the following default sampling strategy to all managed OTel Collectors:

* successful traces (`status_codes = ["OK", "UNSET"]`):
  only 10% of these traces are sampled

* traces containing errors (`status_codes = ["ERROR"]`):
  100% of these traces are sampled

This means that for a service generating 100 succesfull traces/sec only 10 traces/sec will be stored
in the backend together with any other traces that contain errors.

[^1]: <https://opentelemetry.io/>
[^2]: <https://grafana.com/oss/tempo/>
[^3]: <https://opentelemetry.io/docs/languages/>
[^4]: <https://opentelemetry.io/docs/languages/sdk-configuration/>
[^5]: <https://opentelemetry.io/docs/languages/python/exporters/#usage>
