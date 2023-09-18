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
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
)

const defaultK3sPort = "6443"

type dockerCluster struct {
	containerID string
	cli         *client.Client
	cs          *clientset.Clientset
}

func (d *dockerCluster) ClientSet() *clientset.Clientset {
	return d.cs
}

func (d *dockerCluster) start(ctx context.Context) error {
	var err error
	d.cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("failed to create docker client %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	reader, err := d.cli.ImagePull(timeoutCtx, "rancher/k3s:v1.27.5-k3s1", types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	_, err = io.Copy(os.Stdout, reader)
	if err != nil {
		return fmt.Errorf("failed to read from image pull: %w", err)
	}
	_ = reader.Close()

	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:      "rancher/k3s:v1.27.5-k3s1",
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

	if err := d.cli.ContainerStart(ctx, d.containerID, types.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start k3s container: %w", err)
	}

	configFunc := func(context.Context) (bool, error) {
		reader, _, err = d.cli.CopyFromContainer(ctx, d.containerID, kubePath)
		if err == nil {
			return true, nil
		}
		if !errdefs.IsNotFound(err) {
			return false, fmt.Errorf("failed to get kubeconfig from container: %w", err)
		}
		fmt.Println("waiting for k3s kubeconfig...")
		return false, nil
	}
	err = wait.PollUntilContextTimeout(ctx, waitInterval, waitTime, true, configFunc)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig from container after %v", waitTime)
	}

	tarReader := tar.NewReader(reader)
	defer reader.Close()
	_, err = tarReader.Next()
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("k3s kubeConfig is empty")
	}
	if err != nil {
		return fmt.Errorf("failed to untar k3s kubeconfig: %w", err)
	}
	configData, err := io.ReadAll(tarReader)
	if err != nil {
		return fmt.Errorf("failed to read k3s kubeconfig: %w", err)
	}

	restCfg, err := clientcmd.RESTConfigFromKubeConfig(configData)
	if err != nil {
		return fmt.Errorf("failed to create restconfig: %w", err)
	}
	k3sUrl, err := url.Parse(restCfg.Host)
	if err != nil {
		return fmt.Errorf("failed to parse cluster URL: %w", err)
	}
	host, _, err := net.SplitHostPort(k3sUrl.Host)
	if err != nil {
		return fmt.Errorf("failed to parse cluster host: %w", err)
	}
	k3sUrl.Host = net.JoinHostPort(host, cmdFlags.k3sPort)
	restCfg.Host = k3sUrl.String()
	d.cs, err = clientset.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("failed to create new clientset: %w", err)
	}

	// wait for the cluster to become available before creating CRDs
	discFunc := func(context.Context) (bool, error) {
		_, err = d.cs.Discovery().ServerVersion()
		if err == nil {
			return true, nil
		}
		fmt.Println("waiting for k3s cluster...")
		return false, nil
	}
	err = wait.PollUntilContextTimeout(ctx, waitInterval, waitTime, true, discFunc)
	if err != nil {
		return fmt.Errorf("k3s failed to start after %v", waitTime)
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
