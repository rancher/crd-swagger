package cmd

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
	"github.com/rancher/wrangler/v2/pkg/crd"
	"go.uber.org/zap"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

const (
	defaultK3sPort  = "6443"
	defaultK3sImage = "rancher/k3s:v1.27.5-k3s1"
)

type dockerCluster struct {
	containerID string
	cli         *client.Client
	cs          *clientset.Clientset
}

func (d *dockerCluster) start(ctx context.Context) error {
	var err error
	d.cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client %w", err)
	}
	if err = d.pullK3sImage(ctx); err != nil {
		return err
	}
	if err = d.createContainer(ctx); err != nil {
		return err
	}
	if err = d.startContainer(ctx); err != nil {
		return err
	}
	configData, err := d.getKubeCfgFromContainer(ctx)
	if err != nil {
		return err
	}
	restCfg, err := createRESTConfig(configData)
	if err != nil {
		return err
	}

	d.cs, err = clientset.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("failed to create new clientset: %w", err)
	}

	if err := d.waitForCluster(ctx); err != nil {
		return err
	}

	return nil
}

func (d *dockerCluster) stop(ctx context.Context) error {
	defer d.cli.Close()
	// cleanup cluster container
	err := d.cli.ContainerStop(ctx, d.containerID, container.StopOptions{})
	if err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	err = d.cli.ContainerRemove(ctx, d.containerID, types.ContainerRemoveOptions{})
	if err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

func (d *dockerCluster) pullK3sImage(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	reader, err := d.cli.ImagePull(timeoutCtx, cmdFlags.k3sImage, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	if cmdFlags.silent {
		_, err = io.ReadAll(reader)
	} else {
		_, err = io.Copy(os.Stdout, reader)
	}
	if err != nil {
		return fmt.Errorf("failed to read from image pull: %w", err)
	}
	return reader.Close()
}

func (d *dockerCluster) createContainer(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := d.cli.ContainerCreate(timeoutCtx,
		&container.Config{
			Image:      ,
			Entrypoint: []string{"/bin/k3s", "server"},
			ExposedPorts: nat.PortSet{
				defaultK3sPort: struct{}{},
			},
		},
		&container.HostConfig{
			PortBindings: map[nat.Port][]nat.PortBinding{nat.Port(defaultK3sPort): {{HostIP: "127.0.0.1", HostPort: cmdFlags.k3sPort}}},
		}, nil, nil, "crd-swagger")
	if err != nil {
		return fmt.Errorf("failed to create k3s container: %w", err)
	}
	d.containerID = resp.ID
	return nil
}

func (d *dockerCluster) startContainer(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := d.cli.ContainerStart(timeoutCtx, d.containerID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start k3s container: %w", err)
	}
	return nil
}

func (d *dockerCluster) getKubeCfgFromContainer(ctx context.Context) ([]byte, error) {
	var reader io.ReadCloser
	var err error
	configFunc := func(context.Context) (bool, error) {
		timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		reader, _, err = d.cli.CopyFromContainer(timeoutCtx, d.containerID, kubePath)
		if err == nil {
			return true, nil
		}
		if !errdefs.IsNotFound(err) {
			return false, fmt.Errorf("failed to get kubeconfig from container: %w", err)
		}
		zap.S().Info("waiting for k3s kubeconfig...")
		return false, nil
	}
	err = wait.PollUntilContextTimeout(ctx, waitInterval, waitTime, true, configFunc)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig from container after %v", waitTime)
	}

	tarReader := tar.NewReader(reader)
	defer reader.Close()
	_, err = tarReader.Next()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("k3s kubeConfig is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to untar k3s kubeconfig: %w", err)
	}
	configData, err := io.ReadAll(tarReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read k3s kubeconfig: %w", err)
	}
	return configData, nil
}

func createRESTConfig(kubeConfig []byte) (*rest.Config, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeConfig)
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
	k3sURL.Host = net.JoinHostPort(host, cmdFlags.k3sPort)
	restCfg.Host = k3sURL.String()
	return restCfg, nil
}

func (d *dockerCluster) waitForCluster(ctx context.Context) error {
	// wait for the cluster to become available before creating CRDs
	discFunc := func(context.Context) (bool, error) {
		_, err := d.cs.Discovery().ServerVersion()
		if err == nil {
			return true, nil
		}
		zap.L().Info("waiting for k3s cluster...")
		return false, nil
	}
	err := wait.PollUntilContextTimeout(ctx, waitInterval, waitTime, true, discFunc)
	if err != nil {
		return fmt.Errorf("k3s failed to start after %v", waitTime)
	}
	return nil
}

// getClusterSwagger request an openapiv2 document from the cluster and converts it to a spec.Swagger doc for filtering.
func (d *dockerCluster) getSwagger() (*spec.Swagger, error) {
	protoSwagger, err := d.cs.Discovery().OpenAPISchema()
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

// ensureCRD adds the CRDs to the cluster and waits for their status to be ready
func (d *dockerCluster) ensureCRD(ctx context.Context, crds []*apiextv1.CustomResourceDefinition) error {
	crdClient := d.cs.ApiextensionsV1().CustomResourceDefinitions()
	err := crd.BatchCreateCRDs(ctx, crdClient, labels.Everything(), waitTime, crds)
	if err != nil {
		return fmt.Errorf("failed to batch create: %w", err)
	}
	return nil
}
