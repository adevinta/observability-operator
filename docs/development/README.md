# Developing observability-operator

Make sure you have Golang already setup in your local environment.

## Running tests

We have 4 different kinds of tests:
- Standard unit tests at reconciler level
- Linter/formatting tests for embedded configuration files
- Integration tests with GrafanaCloud API
  - Require a valid API key set as env variable GRAFANA_CLOUD_TOKEN
  - Verify the Grafana.com API client works properly (for stack discovery)
- Integration tests for the entire program and Helm chart run in a Kind cluster
  - Require env variable RUN_INTEGRATION_TESTS set to "true"
  - Verify the operator can be deployed and behave correctly inside a k8s cluster

## Setup a local Kubernetes cluster with Kind

For convenience, you may want to run the Operator against a local cluster during development.

Make sure you have a container runtime installed.

For macOS users, consider using `colima`[^1]:

```bash
colima start --edit # set enough resources, ie: 4 cpus and 8gb mem
```

### Create kind cluster

* Create a Kind cluster configuration (ie: `kind-cluster.yaml`):

  ```bash
  cat << EOF > kind-cluster.yaml
  # three node (two workers) cluster config
  kind: Cluster
  apiVersion: kind.x-k8s.io/v1alpha4
  name: observability-operator-demo
  nodes:
  - role: control-plane
    image: kindest/node:v1.30.0
  - role: worker
    image: kindest/node:v1.30.0
  EOF
  ```

* Create the Kind cluster:

  ```bash
  kind create cluster --config kind-cluster.yaml
  ```

### Setup Operator runtime dependencies

> [!IMPORTANT]
> Make sure you are using the kubectl context of your kind cluster, ie:
>
> ```bash
> kubectl config use-context kind-observability-operator-demo # or equivalent with kubectx
> ```

* Namespaces

  ```bash
  kubectl create namespace platform-services      # for Prometheus instances and operator 
  kubectl create namespace observability          # for Alloy instances
  kubectl create namespace userns-dev             # for user namespace (example)
  kubectl create namespace observability-operator # where Operator pods run (if installed via helm)
  ```

* Prometheus Operator

  ```bash
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
  helm install prometheus-operator prometheus-community/kube-prometheus-stack -n platform-services
  ```

* Kubernetes Metrics Server

  ```bash
  curl -L -O https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml

  # edit components.yaml:
  #   add `- --kubelet-insecure-tls` arg to "metrics-server" container 

  kubectl apply -f components.yaml
  ```

  You should now be able to run `kubectl top pod` against your kind cluster.

* Vertical Pod Autoscaler (VPA)

  ```bash
  git clone https://github.com/kubernetes/autoscaler.git

  # fix addext param for openssl version https://github.com/kubernetes/autoscaler/issues/6126if 
  OPENSSL_VERSION=$(openssl version | awk '{print $2}')
  if [[ ! $OPENSSL_VERSION =~ ^1\.1\..* ]]; then
    echo "OpenSSL version is not 1.1.x, modifying the file..."
    sed -i 's/-addext "subjectAltName = DNS:${CN_BASE}_ca"//g' autoscaler/vertical-pod-autoscaler/pkg/admission-controller/gencerts.sh
  else
    echo "OpenSSL version is 1.1.x, no modification needed."
  fi

  # Make the autoscaler up
  ./autoscaler/vertical-pod-autoscaler/hack/vpa-up.sh
  # check vpa-admission-controller is running
  kubectl get pods -n kube-system 
  ```

## Run the Operator

> [!IMPORTANT]
> Make sure you are using the kubectl context of your kind cluster, ie:
>
> ```bash
> kubectl config use-context kind-observability-operator-demo # or equivalent with kubectx
> ```

### A. Locally against your local kind cluster

Tokens must be set but they can be fake if you don't need to test the Grafana Cloud integration.

```bash
GRAFANA_CLOUD_TOKEN='test1234' GRAFANA_CLOUD_TRACES_TOKEN='123455'\
go run cmd/observability-operator/main.go\
    -cluster-name='observability-operator-demo'\
    -cluster-region='eu-west-1'\
    -cluster-environment='dev'\
    -cluster-domain='adevinta.com'
```

### B. In-cluster (within your local kind cluster)

```bash
docker build -t local/adevinta/observability-operator:latest .
kind load docker-image --name observability-operator-demo local/adevinta/observability-operator:latest

cat << EOF > secrets.yaml 
kind: Secret
type: Opaque
apiVersion: v1
metadata:
  name: observability-operator-grafana-cloud-credentials
  namespace: observability-operator
data:
  grafana-cloud-api-key: YOUR_BASE64_ENCODED_TOKEN
  grafana-cloud-traces-token: YOUR_BASE64_ENCODED_TOKEN
EOF

kubectl apply -f secrets.yaml 

helm upgrade observability-operator helm-chart/observability-operator --install --namespace observability-operator\
  --set image.registry=local\
  --set image.repository=adevinta/observability-operator\
  --set image.tag=latest\
  --set image.pullPolicy=Never\
  --set enableSelfVpa=false\
  --set clusterName=observability-operator-demo\
  --set region=eu-west-1\
  --set clusterDomain=adevinta.com\
  --set excludeNamespaces=kube-system\
  --set credentials.GRAFANA_CLOUD_TOKEN.secretName=observability-operator-grafana-cloud-credentials\
  --set credentials.GRAFANA_CLOUD_TRACES_TOKEN.secretName=observability-operator-grafana-cloud-credentials
```

## [Optional] Debug the Operator with VS Code

If you are using VS Code you can use the following "launch configuration"[^2] to start a Debugging session from 
within your editor:

```json
// .vscode/launch.json
{
    "version": "0.2.0",
    "configurations": [        
        {
            "name": "Run Observability Operator",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${workspaceFolder}/cmd/observability-operator/main.go",
            "args": [
                "-cluster-name", "observability-operator-demo",
                "-cluster-region", "eu-west-1",
                "-cluster-environment", "dev",
            ],
            "env": { 
                "GRAFANA_CLOUD_TOKEN": "test1234",
                "GRAFANA_CLOUD_TRACES_TOKEN": "test1234"
            },
            "console": "integratedTerminal",
            "preLaunchTask": "check-kubectl-context"
        }
    ]
}
```

The `check-kubectl-context` preLaunchTask might be useful to ensure you are running against the right kubernetes cluster.

You may define this task as follows:

```json
// your workspace settings
{
    "folders": [
        {
            "path": "path/to/observability-operator"
        }
    ],
    "settings": {},
    "tasks": {
        "version": "2.0.0",
        "tasks": [
            {
                "label": "check-kubectl-context",
                "type": "shell",
                "command": "[[ $(kubectl config current-context) == 'kind-observability-operator-demo' ]] || exit 1"
            }
        ]
    }
}
```

[^1]: <https://github.com/abiosoft/colima/blob/main/docs/INSTALL.md>
[^2]: <https://github.com/golang/vscode-go/wiki/debugging#launch>
