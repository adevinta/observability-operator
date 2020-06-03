package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	k8s "github.com/adevinta/go-k8s-toolkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/support/kind"
	"sigs.k8s.io/e2e-framework/third_party/helm"
)

var (
	testenv         env.Environment
	imageRegistry   = "local"
	imageRepository = "adevinta/observability-operator"
	imageTag        = "latest"
	imageFullUrl    = imageRegistry + "/" + imageRepository + ":" + imageTag
	kindClusterName = envconf.RandomName("observability", 16)
	releaseName     = "observability-operator"

	operatorNamespace   = "observability-operator"
	prometheusNamespace = "platform-services"
	tracesNamespace     = "observability"
)

func buildLocalObservabilityOperatorImage(t *testing.T) {
	// Ensure we always have the latest version of the code compiled
	// This is crucial when running integration tests locally
	cmd := exec.Command("docker", "build", "-t", imageFullUrl, ".")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Dir = "../../"
	require.NoError(t, cmd.Run())

	cmd = exec.Command("kind", "load", "docker-image", "--name", kindClusterName, imageFullUrl)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Dir = "../../"
	require.NoError(t, cmd.Run())
}

func installObservabilityOperator(t *testing.T, args ...string) {
	t.Helper()
	helmClient := helm.New(testenv.EnvConf().KubeconfigFile())

	require.NoError(t, helmClient.RunUpgrade(
		helm.WithName(releaseName),
		helm.WithNamespace(operatorNamespace),
		helm.WithChart("../../helm-chart/observability-operator"),
		helm.WithArgs(
			"--install",
		),
		helm.WithArgs(args...),
	))
}

// Verify the Helm chart can complete successfully, the pod can be
// scheduled and get to ready. The Pod may fail right afterwards due
// to missing credentials or values, but it was able to start-up, so
// the CLI flags, Helm values and immediate verifications on startup
// passed.
func TestDeployDefaultHelmChart(t *testing.T) {
	t.Setenv("KUBECONFIG", testenv.EnvConf().KubeconfigFile())

	secretName := "observability-operator-grafana-cloud-credentials"

	buildLocalObservabilityOperatorImage(t)
	installObservabilityOperator(
		t,
		"--set", "image.registry="+imageRegistry,
		"--set", "image.repository="+imageRepository,
		"--set", "image.tag="+imageTag,
		"--set", "image.pullPolicy=Never",
		"--set", "enableSelfVpa=false",
		"--set", "namespaces.tracesNamespace.name="+tracesNamespace,
		"--set", "namespaces.prometheusNamespace.name="+prometheusNamespace,
		"--set", "credentials.GRAFANA_CLOUD_TOKEN.secretName="+secretName,
		"--set", "credentials.GRAFANA_CLOUD_TRACES_TOKEN.secretName="+secretName,
	)

	cfg, err := k8s.NewClientConfigBuilder().WithKubeConfigPath(testenv.EnvConf().KubeconfigFile()).Build()
	require.NoError(t, err)
	k8sClient, err := client.New(cfg, client.Options{})
	require.NoError(t, err)

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: operatorNamespace,
		},
		StringData: map[string]string{
			"grafana-cloud-api-key":      "",
			"grafana-cloud-traces-token": "",
		},
	}
	err = k8sClient.Create(context.Background(), &secret)
	require.NoError(t, err)

	assert.Eventually(
		t,
		func() bool {
			podList := corev1.PodList{}
			err = client.NewNamespacedClient(k8sClient, operatorNamespace).List(context.Background(), &podList)
			require.NoError(t, err)

			require.Len(t, podList.Items, 1)
			return strings.HasPrefix(podList.Items[0].Name, releaseName)
		},
		30*time.Second, 10*time.Millisecond,
		"The operator pod should be present",
	)

	assert.Eventually(
		t,
		func() bool {
			podList := corev1.PodList{}
			err = client.NewNamespacedClient(k8sClient, operatorNamespace).List(context.Background(), &podList)
			require.NoError(t, err)

			require.Len(t, podList.Items, 1)
			return corev1.PodPhase("Running") == podList.Items[0].Status.Phase
		},
		30*time.Second, 10*time.Millisecond,
		"The operator pod should be running",
	)
}

func TestMain(m *testing.M) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "true" {
		fmt.Printf("RUN_INTEGRATION_TESTS is not set, so skipping all tests of main")
		os.Exit(0)
	}

	testenv = env.New()
	// Use pre-defined environment funcs to create a kind cluster prior to test run
	testenv.Setup(
		envfuncs.CreateCluster(kind.NewCluster(kindClusterName), kindClusterName),
		envfuncs.CreateNamespace(operatorNamespace),
		envfuncs.CreateNamespace(prometheusNamespace),
		envfuncs.CreateNamespace(tracesNamespace),
	)

	// Use pre-defined environment funcs to teardown kind cluster after tests
	testenv.Finish(
		envfuncs.DeleteNamespace(operatorNamespace),
		envfuncs.DestroyCluster(kindClusterName),
	)

	// launch package tests
	os.Exit(testenv.Run(m))
}
