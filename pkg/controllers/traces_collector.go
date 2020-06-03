package controllers

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"text/template"

	appsv1 "k8s.io/api/apps/v1"
	autoscaling "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultObjPrefix     string = "alloy-"
	defaultServicePrefix string = "otelcol-"
)

// TracesCollector abstracts the management of the Alloy service and
// its related objects. The goal is to create an Alloy deployment and
// service for each namespace that sends traces. The management of a
// single Alloy is represented by this type and the orchestration of
// the different Alloy services is managed elsewhere.
type TracesCollector struct {
	Name            string
	HTTPPort        int32
	ServicePorts    []servicePort
	TraceNamespace  string
	TenantNamespace string
	ClusterName     string
	EnableVPA       bool

	templatedConfig string

	Secret        *corev1.Secret
	ConfigMap     *corev1.ConfigMap
	Service       *corev1.Service
	Deployment    *appsv1.Deployment
	NetworkPolicy *networkingv1.NetworkPolicy
	VPA           *vpav1.VerticalPodAutoscaler
}

// TracesCollectorOption implements the functional options pattern for
// TracesCollector
type TracesCollectorOption func(*TracesCollector) error

// WithServicePorts allows to specify a list of Service Ports that
// should be enabled.
func WithServicePorts(extraPorts ...servicePort) TracesCollectorOption {
	return func(tc *TracesCollector) error {
		tc.ServicePorts = append(tc.ServicePorts, extraPorts...)
		return nil
	}
}

// WithHTTPPort allows to specify which is the port used to expose the
// HTTP interface of Alloy
func WithHTTPPort(port int32) TracesCollectorOption {
	return func(tc *TracesCollector) error {
		if tc.HTTPPort != 0 {
			if tc.HTTPPort == port {
				// nothing to do
				return nil
			}

			// Remove prior port from existing ServicePort list if it was present
			var newServicePorts []servicePort
			for i, v := range tc.ServicePorts {
				if v.Port == tc.HTTPPort {
					newServicePorts = append(tc.ServicePorts[:i], tc.ServicePorts[i+1:]...)
				}
			}
			tc.ServicePorts = newServicePorts
		}

		tc.HTTPPort = port
		tc.ServicePorts = append(tc.ServicePorts, servicePort{Name: "http", Port: httpPort})
		return nil
	}
}

// AlloyWithOTelCredentials renders the Alloy configuration file using
// the supplied credentials to set up the destination of traces
func AlloyWithOTelCredentials(credentials ...OTelCredentials) TracesCollectorOption {
	return func(tc *TracesCollector) error {
		templatedConfig, err := tc.getAlloyConfigContents(credentials...)
		if err != nil {
			return err
		}
		tc.templatedConfig = templatedConfig
		return nil
	}
}

// NewTracesCollector initializes the TracesCollector type.
func NewTracesCollector(tenantNamespace, traceNamespace, clusterName string, enableVPA bool, opts ...TracesCollectorOption) (*TracesCollector, error) {
	tc := &TracesCollector{
		Name:            tenantNamespace,
		HTTPPort:        httpPort,
		ServicePorts:    []servicePort{{Name: "http", Port: httpPort}},
		TraceNamespace:  traceNamespace,
		TenantNamespace: tenantNamespace,
		ClusterName:     clusterName,
		EnableVPA:       enableVPA,
	}
	for _, opt := range opts {
		err := opt(tc)
		if err != nil {
			return nil, err
		}
	}
	return tc, nil
}

func (tc *TracesCollector) objName() string {
	return defaultObjPrefix + tc.Name
}

func (tc *TracesCollector) svcName() string {
	return defaultServicePrefix + tc.Name
}

func (tc *TracesCollector) labelMap() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       tc.objName(),
		"app.kubernetes.io/managed-by": "grafana-cloud-operator",
		"app.kubernetes.io/version":    "v1.2.1",
	}
}

func (tc *TracesCollector) annotationMap() map[string]string {
	return map[string]string{
		"prometheus.io/path":   "/metrics",
		"prometheus.io/port":   strconv.Itoa(int(tc.HTTPPort)),
		"prometheus.io/scrape": "true",
	}
}

// defineVPA returns a default initialized VPA object and
// populates it inside the TracesCollector as well
func (tc *TracesCollector) defineVPA() *vpav1.VerticalPodAutoscaler {
	var updateMode vpav1.UpdateMode = "Auto"

	tc.VPA = &vpav1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.objName(),
			Namespace: tc.TraceNamespace,
		},
		Spec: vpav1.VerticalPodAutoscalerSpec{
			TargetRef: &autoscaling.CrossVersionObjectReference{
				Kind: "Deployment",
				Name: tc.objName(),
			},
			UpdatePolicy: &vpav1.PodUpdatePolicy{
				UpdateMode: &updateMode,
			},
		},
	}
	return tc.VPA
}

// CreateOrUpdateVPA creates or updates the VPA by calling K8s API
func (tc *TracesCollector) CreateOrUpdateVPA(ctx context.Context, client client.Client) error {
	vpaMutating := tc.defineVPA().DeepCopy()

	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		vpaMutating,
		func() error {
			vpaMutating.Spec = tc.VPA.Spec
			return nil
		},
	)
	return err
}

// defineSecret returns a default initialized secret object and
// populates it inside the TracesCollector as well
func (tc *TracesCollector) defineSecret() *corev1.Secret {
	tc.Secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.objName(),
			Namespace: tc.TraceNamespace,
			Labels:    tc.labelMap(),
		},
		Type: corev1.SecretTypeOpaque,
	}
	return tc.Secret
}

// CreateOrUpdateSecret creates or updates the secret by calling K8s API
func (tc *TracesCollector) CreateOrUpdateSecret(ctx context.Context, client client.Client, grafanaCloudToken string) error {
	secretMutating := tc.defineSecret().DeepCopy()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		secretMutating,
		func() error {
			secretMutating.Data = map[string][]byte{
				"grafana-cloud-traces-token": []byte(grafanaCloudToken),
			}
			return nil
		},
	)
	return err
}

// defineConfigMap returns a default initialized configmap object and
// populates it inside the TracesCollector as well
func (tc *TracesCollector) defineConfigMap() *corev1.ConfigMap {
	tc.ConfigMap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.objName(),
			Namespace: tc.TraceNamespace,
			Labels:    tc.labelMap(),
		},
		Data: map[string]string{
			"config.alloy": tc.templatedConfig,
		},
	}
	return tc.ConfigMap
}

// CreateOrUpdateConfigMap creates or updates the configMap by calling K8s API
func (tc *TracesCollector) CreateOrUpdateConfigMap(ctx context.Context, client client.Client) error {
	configMapMutating := tc.defineConfigMap().DeepCopy()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		configMapMutating,
		func() error {
			configMapMutating.Data = tc.ConfigMap.Data
			return nil
		},
	)
	return err
}

// defineService returns a default initialized service object and
// populates it inside the TracesCollector as well
func (tc *TracesCollector) defineService() *corev1.Service {
	serviceName := tc.svcName()
	labels := tc.labelMap()

	var ports []corev1.ServicePort
	for _, sp := range tc.ServicePorts {
		ports = append(
			ports,
			corev1.ServicePort{
				Name:     serviceName + "-" + sp.Name,
				Protocol: corev1.ProtocolTCP,
				Port:     sp.Port,
				TargetPort: intstr.IntOrString{
					IntVal: sp.Port,
				},
			},
		)
	}
	tc.Service = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: tc.TraceNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			// service name is the tenant namespace so that svc endpoint looks like '<tenant-ns>.observability.svc.cluster.local'
			// the service selector should target the alloy pods with name "alloy-<tenant-ns>"
			Selector: map[string]string{
				"app.kubernetes.io/name": labels["app.kubernetes.io/name"],
			},
			Ports: ports,
		},
	}

	return tc.Service
}

// CreateOrUpdateService creates or updates the service by calling K8s API
func (tc *TracesCollector) CreateOrUpdateService(ctx context.Context, client client.Client) error {
	serviceMutating := tc.defineService().DeepCopy()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		serviceMutating,
		func() error {
			serviceMutating.Spec.Ports = tc.Service.Spec.Ports
			return nil
		},
	)
	return err
}

// defineDeployment returns a default initialized Alloy deployment
// object and populates it inside the TracesCollector as well
func (tc *TracesCollector) defineDeployment() *appsv1.Deployment {
	name := tc.objName()
	numReplicas := int32(1)

	CPULimit := resource.MustParse("250m")
	CPURequest := resource.MustParse("50m")

	MemoryLimit := resource.MustParse("500Mi")
	MemoryRequest := resource.MustParse("250Mi")

	var ports []corev1.ContainerPort
	for _, sp := range tc.ServicePorts {
		ports = append(
			ports,
			corev1.ContainerPort{
				ContainerPort: sp.Port,
				Name:          sp.Name,
			},
		)
	}

	tc.Deployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tc.TraceNamespace,
			Labels:    tc.labelMap(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      tc.labelMap(),
					Annotations: tc.annotationMap(),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: name,
							Args: []string{
								"run",
								"/etc/alloy/config.alloy",
								"--storage.path=/tmp/alloy",
								fmt.Sprintf("--server.http.listen-addr=0.0.0.0:%v", tc.HTTPPort),
								"--server.http.ui-path-prefix=/",
								"--stability.level=generally-available",
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    CPULimit,
									corev1.ResourceMemory: MemoryLimit,
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    CPURequest,
									corev1.ResourceMemory: MemoryRequest,
								},
							},
							Image: "docker.io/grafana/alloy:v1.2.1",
							Ports: ports,
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/-/ready",
										Port:   intstr.IntOrString{IntVal: tc.HTTPPort},
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
								TimeoutSeconds:      2,
								FailureThreshold:    3,
								SuccessThreshold:    1,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path:   "/-/ready",
										Port:   intstr.IntOrString{IntVal: tc.HTTPPort},
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
								TimeoutSeconds:      2,
								FailureThreshold:    3,
								SuccessThreshold:    1,
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/etc/alloy",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "GRAFANA_CLOUD_TRACES_TOKEN",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: tc.objName(),
											},
											Key: "grafana-cloud-traces-token",
										},
									},
								},
							},
						},
						{
							Name:  "config-reloader",
							Image: "ghcr.io/jimmidyson/configmap-reload:v0.12.0",
							Args: []string{
								"--volume-dir=/etc/alloy",
								fmt.Sprintf("--webhook-url=http://localhost:%v/-/reload", tc.HTTPPort),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: "/etc/alloy",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: tc.objName(),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	return tc.Deployment
}

// CreateOrUpdateDeployment creates or updates the deployment by calling K8s API
func (tc *TracesCollector) CreateOrUpdateDeployment(ctx context.Context, client client.Client) error {
	deploymentMutating := tc.defineDeployment().DeepCopy()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		deploymentMutating,
		func() error {
			deploymentMutating.Spec = tc.Deployment.Spec
			return nil
		},
	)
	return err
}

// defineNetworkPolicy returns a default initialized network policy
// object restricted to a single source namespace and populates it
// inside the TracesCollector as well
func (tc *TracesCollector) defineNetworkPolicy() *networkingv1.NetworkPolicy {
	name := tc.objName()
	protocol := corev1.ProtocolTCP

	var ports []networkingv1.NetworkPolicyPort
	for _, port := range tc.ServicePorts {
		ports = append(
			ports,
			networkingv1.NetworkPolicyPort{
				Protocol: &protocol,
				Port:     &intstr.IntOrString{IntVal: port.Port},
			},
		)
	}

	tc.NetworkPolicy = &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tc.TraceNamespace,
			Labels:    tc.labelMap(),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": tc.Name,
								},
							},
						},
					},
					Ports: ports,
				},
			},
		},
	}
	return tc.NetworkPolicy
}

// CreateOrUpdateNetworkPolicy creates or updates the network policy by calling K8s API
func (tc *TracesCollector) CreateOrUpdateNetworkPolicy(ctx context.Context, client client.Client) error {
	networkPolicyMutating := tc.defineNetworkPolicy().DeepCopy()
	_, err := ctrl.CreateOrUpdate(
		ctx,
		client,
		networkPolicyMutating,
		func() error {
			networkPolicyMutating.Spec = tc.NetworkPolicy.Spec
			return nil
		},
	)
	return err
}

// ObjectsToDelete returns a list of all the Alloy deployment related
// objects for deletion. The TracesColector has to be initialized the
// same way as it was when creating the initial objects for the
// deletion to succeed.
func (tc *TracesCollector) ObjectsToDelete() []client.Object {
	objects := []client.Object{
		tc.defineSecret(),
		tc.defineConfigMap(),
		tc.defineService(),
		tc.defineDeployment(),
		tc.defineNetworkPolicy(),
		tc.defineVPA(),
	}

	if tc.EnableVPA {
		objects = append(objects, tc.defineVPA())
	}

	return objects
}

type servicePort struct {
	Name string
	Port int32
}

//go:embed files/config.alloy
var alloyConfigTemplate string

type alloyConfig struct {
	Credentials              []OTelCredentials
	CustomResourceAttributes map[string]string
}

func (tc *TracesCollector) getAlloyConfigContents(credentials ...OTelCredentials) (string, error) {
	// Set up default credential values
	var cred []OTelCredentials
	for _, v := range credentials {
		if v.Password == "" {
			v.Password = "env(\"GRAFANA_CLOUD_TRACES_TOKEN\")"
		}
		cred = append(cred, v)
	}

	// Render the template
	config := alloyConfig{
		Credentials: cred,
		CustomResourceAttributes: map[string]string{
			"k8s.cluster.name":   tc.ClusterName,
			"k8s.namespace.name": tc.TenantNamespace,
		},
	}
	tpl := template.New("alloy-config")
	tpl, err := tpl.Parse(alloyConfigTemplate)
	if err != nil {
		return "", err
	}

	var buffer bytes.Buffer
	err = tpl.Execute(&buffer, config)
	if err != nil {
		return "", err
	}
	return buffer.String(), nil
}

type OTelCredentials struct {
	User, Password, Endpoint string
}
