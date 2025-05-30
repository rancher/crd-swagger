package cmd

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

type kubeClient struct {
	cs *clientset.Clientset
}

const (
	discoveryPollInterval = 10 * time.Second
	discoveryPollTimeout  = 5 * time.Minute
)

func newKubeClient(restConfig []byte) (*kubeClient, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create restconfig: %w", err)
	}
	k3sURL, err := url.Parse(restCfg.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cluster URL: %w", err)
	}
	host, _, err := net.SplitHostPort(k3sURL.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cluster host: %w", err)
	}
	k3sURL.Host = net.JoinHostPort(host, defaultK3sPort)
	restCfg.Host = k3sURL.String()

	cs, err := clientset.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create new clientset: %w", err)
	}

	return &kubeClient{
		cs: cs,
	}, nil

}

func (kc *kubeClient) getSwagger() (*spec.Swagger, error) {
	protoSwagger, err := kc.cs.Discovery().OpenAPISchema()
	if err != nil {
		return nil, fmt.Errorf("failed to get swagger from cluster: %w", err)
	}
	var swagger spec.Swagger
	ok, err := swagger.FromGnostic(protoSwagger)
	if err != nil || !ok {
		return nil, fmt.Errorf("failed to convert protoSwagger struct: %w", err)
	}
	return &swagger, nil
}

func (kc *kubeClient) waitForDesiredResources(ctx context.Context, desiredResources map[metav1.GroupKind]bool, logger *zap.SugaredLogger) error {
	GKfound := make(map[metav1.GroupKind]bool)
	for gk := range desiredResources {
		GKfound[gk] = false
	}

	waitFunc := func(ctc context.Context) (bool, error) {
		apiResourceList, err := kc.cs.Discovery().ServerPreferredResources()
		if err != nil {
			logger.Errorf("Discovery API call failed (will retry): %v", err)
			return false, nil
		}

		foundNewGK := false
		for _, apiResource := range apiResourceList {
			gv, err := schema.ParseGroupVersion(apiResource.GroupVersion)
			if err != nil {
				logger.Warnf("Skipping invalid GroupVersion '%s': %v", apiResource.GroupVersion, err)
				continue
			}

			for _, resource := range apiResource.APIResources {
				discoveredGK := metav1.GroupKind{
					Group: gv.Group,
					Kind:  resource.Kind,
				}

				if _, ok := GKfound[discoveredGK]; ok && !GKfound[discoveredGK] {
					logger.Infof("Found GroupKind '%s' in API resource '%s'", discoveredGK.String(), resource.Name)
					GKfound[discoveredGK] = true
					foundNewGK = true
				}
			}
		}

		allFound := true
		for _, found := range GKfound {
			if !found {
				allFound = false
				break
			}
		}

		if allFound {
			logger.Info("All desired GroupKinds found in API resources")
			return true, nil
		}

		if foundNewGK {
			logger.Infof("Still waiting for %d GroupKind(s): %s",
				countNotFound(GKfound), formatNotFoundGroupKinds(GKfound))
		} else {
			logger.Debugf("No GroupKinds discovered in this poll. Still waiting for %d.",
				countNotFound(GKfound))
		}

		return false, nil
	}
	err := wait.PollUntilContextTimeout(ctx, discoveryPollInterval, discoveryPollTimeout, true, waitFunc)
	if err != nil {
		logger.Errorf("Timed out waiting for all GroupKinds. Still missing: %s",
			formatNotFoundGroupKinds(GKfound))
		return fmt.Errorf("not all GroupKinds discoverable after %v. Error: %w",
			discoveryPollTimeout, err)
	}

	logger.Info("All desired GroupKinds are discoverable.")
	return nil
}

// Helper to count how many GroupKinds are not yet found
func countNotFound(GKfound map[metav1.GroupKind]bool) int {
	count := 0
	for _, found := range GKfound {
		if !found {
			count++
		}
	}
	return count
}

// Helper to format all GroupKinds in a map
func formatGroupKinds(GKfound map[metav1.GroupKind]bool) string {
	if len(GKfound) == 0 {
		return "None"
	}
	keys := make([]string, 0, len(GKfound))
	for k := range GKfound {
		keys = append(keys, k.String())
	}
	return strings.Join(keys, ", ")
}

// Helper to format only GroupKinds that haven't been found yet
func formatNotFoundGroupKinds(GKfound map[metav1.GroupKind]bool) string {
	notFound := make([]string, 0)
	for gk, found := range GKfound {
		if !found {
			notFound = append(notFound, gk.String())
		}
	}
	if len(notFound) == 0 {
		return "None"
	}
	return strings.Join(notFound, ", ")
}
