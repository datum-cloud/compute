// Package env provides helpers for connecting to the local Kind+Karmada e2e
// environment created by "task e2e:up".
//
// # Environment layout
//
// The environment consists of three Kind clusters and one Karmada API server:
//
//   - Control plane cell  — hosts the compute operator (WorkloadReconciler,
//     WorkloadDeploymentFederator, InstanceProjector).
//   - Karmada API server  — the federation control plane; WorkloadDeployments
//     are written here so Karmada can propagate them to POP cells.
//   - POP DFW (compute-pop-dfw) — member cluster labelled city-code=dfw.
//   - POP ORD (compute-pop-ord) — member cluster labelled city-code=ord.
//
// # Kubeconfig resolution
//
// Kubeconfigs are read from the directory at [DefaultKubeconfigDir] (relative
// to the repository root), unless overridden via the [EnvKubeconfigDir]
// environment variable.
//
// Expected files inside that directory:
//
//	control-plane.yaml   — management / control-plane cell
//	karmada.yaml         — Karmada federation API server (https://localhost:32443)
//	pop-dfw.yaml         — POP DFW cell (standard Kind localhost-based kubeconfig)
//	pop-ord.yaml         — POP ORD cell (standard Kind localhost-based kubeconfig)
//
// # Typical usage in a Ginkgo suite
//
//	var (
//	    testEnv *env.Environment
//	)
//
//	var _ = BeforeSuite(func() {
//	    scheme := runtime.NewScheme()
//	    Expect(computev1alpha1.AddToScheme(scheme)).To(Succeed())
//	    Expect(corev1.AddToScheme(scheme)).To(Succeed())
//
//	    var err error
//	    testEnv, err = env.New(scheme)
//	    Expect(err).NotTo(HaveOccurred())
//	})
package env

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Environment variable name that overrides the kubeconfig directory.
const EnvKubeconfigDir = "E2E_KUBECONFIG_DIR"

// DefaultKubeconfigDir is the kubeconfig directory used when [EnvKubeconfigDir]
// is not set. It is resolved relative to the repository root (three directories
// above this source file).
const DefaultKubeconfigDir = "tmp/e2e/kubeconfigs"

// City codes for the two POP cells created by "task e2e:up".
const (
	CityCodeDFW = "dfw"
	CityCodeORD = "ord"
)

// Environment holds a [ClusterAccess] for each cluster in the local e2e
// environment. All fields are populated by [New]; none are nil on success.
type Environment struct {
	// ControlPlane is the management / control-plane cell cluster.
	// The compute operator runs here (WorkloadReconciler,
	// WorkloadDeploymentFederator, InstanceProjector).
	ControlPlane *ClusterAccess

	// Karmada is the Karmada federation API server.
	// WorkloadDeployments and PropagationPolicies live here.
	Karmada *ClusterAccess

	// POPCells maps city-code strings (e.g. "dfw", "ord") to the
	// corresponding POP cell cluster. Use [Environment.POPCell] for
	// safe, error-returning access.
	POPCells map[string]*ClusterAccess
}

// ClusterAccess bundles a REST config and a controller-runtime Client for a
// single cluster.
type ClusterAccess struct {
	// Config is the REST config used to build the client.
	Config *rest.Config

	// Client is a controller-runtime client scoped to this cluster.
	// The client is built with the scheme supplied to [New].
	Client ctrlclient.Client
}

// New creates an [Environment] by loading kubeconfigs from the configured
// directory and building a controller-runtime client for each cluster using
// the provided scheme.
//
// The scheme should have all relevant types registered before calling New;
// for example compute types, networking types, and core Kubernetes types.
func New(scheme *k8sruntime.Scheme) (*Environment, error) {
	dir := kubeconfigDir()

	controlPlane, err := loadCluster(filepath.Join(dir, "control-plane.yaml"), scheme)
	if err != nil {
		return nil, fmt.Errorf("control-plane cluster: %w", err)
	}

	karmada, err := loadCluster(filepath.Join(dir, "karmada.yaml"), scheme)
	if err != nil {
		return nil, fmt.Errorf("karmada API server: %w", err)
	}

	popDFW, err := loadCluster(filepath.Join(dir, "pop-dfw.yaml"), scheme)
	if err != nil {
		return nil, fmt.Errorf("POP DFW cluster: %w", err)
	}

	popORD, err := loadCluster(filepath.Join(dir, "pop-ord.yaml"), scheme)
	if err != nil {
		return nil, fmt.Errorf("POP ORD cluster: %w", err)
	}

	return &Environment{
		ControlPlane: controlPlane,
		Karmada:      karmada,
		POPCells: map[string]*ClusterAccess{
			CityCodeDFW: popDFW,
			CityCodeORD: popORD,
		},
	}, nil
}

// POPCell returns the [ClusterAccess] for the POP cell with the given city
// code. It returns an error if no POP cell is registered for that code.
func (e *Environment) POPCell(cityCode string) (*ClusterAccess, error) {
	ca, ok := e.POPCells[cityCode]
	if !ok {
		known := make([]string, 0, len(e.POPCells))
		for k := range e.POPCells {
			known = append(known, k)
		}
		return nil, fmt.Errorf("no POP cell registered for city code %q (known: %v)", cityCode, known)
	}
	return ca, nil
}

// MustPOPCell is like [Environment.POPCell] but panics on error.
// Useful in test setup where a missing POP cell is always a fatal misconfiguration.
func (e *Environment) MustPOPCell(cityCode string) *ClusterAccess {
	ca, err := e.POPCell(cityCode)
	if err != nil {
		panic(err)
	}
	return ca
}

// RESTConfigFor is a convenience function that returns a [rest.Config] for the
// named cluster without constructing a client. Useful when the caller needs to
// build a typed clientset (e.g. karmada-io/client-go) directly.
func RESTConfigFor(kubeconfigPath string) (*rest.Config, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building REST config from %s: %w", kubeconfigPath, err)
	}
	return cfg, nil
}

// KubeconfigPath returns the absolute path to the kubeconfig file for the
// named cluster. name must be one of "control-plane", "karmada", "pop-dfw",
// or "pop-ord".
func KubeconfigPath(name string) string {
	return filepath.Join(kubeconfigDir(), name+".yaml")
}

// ─── internal helpers ────────────────────────────────────────────────────────

func loadCluster(kubeconfigPath string, scheme *k8sruntime.Scheme) (*ClusterAccess, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building REST config from %s: %w", kubeconfigPath, err)
	}

	c, err := ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("building client from %s: %w", kubeconfigPath, err)
	}

	return &ClusterAccess{
		Config: cfg,
		Client: c,
	}, nil
}

// kubeconfigDir returns the directory containing e2e kubeconfigs.
// It honours the E2E_KUBECONFIG_DIR environment variable, otherwise falls
// back to <repo-root>/tmp/e2e/kubeconfigs.
func kubeconfigDir() string {
	if dir := os.Getenv(EnvKubeconfigDir); dir != "" {
		return dir
	}
	return filepath.Join(repoRoot(), DefaultKubeconfigDir)
}

// repoRoot walks up from this source file to find the repository root
// (identified by the presence of go.mod).
func repoRoot() string {
	// Use the file path of this source file as a starting point so the helper
	// works regardless of the caller's working directory.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		// Fallback: assume tests are run from the repo root.
		return "."
	}

	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding go.mod.
			return "."
		}
		dir = parent
	}
}
