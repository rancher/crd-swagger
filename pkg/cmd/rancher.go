package cmd

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	rancherDefaultImage   = "rancher/rancher"
	defaultRancherVersion = "latest"

	containerPort        = "80/tcp"
	containerPortHTTPS   = "443/tcp"
	defaultHostPort      = "80"
	defaultHostPortHTTPS = "443"
	containerName        = "rancher-crd-swagger"
	defaultK3sPort       = "6443"

	requestTimeout = time.Minute * 10
	waitInterval   = time.Millisecond * 500
	waitTime       = time.Minute * 10

	kubePath = "/etc/rancher/k3s/k3s.yaml"
)

type rancherDockerContainer struct {
	image         string
	containerName string

	hostPort      string
	hostPortHTTPS string

	containerID  string
	dockerClient *docker.Client

	ctx    context.Context
	logger *zap.SugaredLogger
}

func newRancherDockerContainer(ctx context.Context, logger *zap.SugaredLogger, image, version, hostPort, hostPortHTTPS string) (*rancherDockerContainer, error) {
	dockerClient, err := docker.NewClientWithOpts(docker.FromEnv, docker.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	rancherContainer := &rancherDockerContainer{
		ctx:    ctx,
		logger: logger,

		dockerClient:  dockerClient,
		containerName: containerName + uuid.New().String(),
	}

	if image != "" {
		rancherContainer.image = image
	} else {
		if version == "" {
			version = defaultRancherVersion
		}
		rancherContainer.image = rancherDefaultImage + ":" + version
	}

	if hostPort == "" {
		hostPort = defaultHostPort
	}

	if hostPortHTTPS == "" {
		hostPortHTTPS = defaultHostPortHTTPS
	}

	// Validate host ports
	if hostPort == hostPortHTTPS {
		return nil, fmt.Errorf("host port and host port https cannot be the same")
	}

	rancherContainer.hostPort = hostPort
	rancherContainer.hostPortHTTPS = hostPortHTTPS
	rancherContainer.containerName = containerName + uuid.New().String()

	return rancherContainer, nil
}

func (r *rancherDockerContainer) start() error {
	// Pull the rancher image
	if err := r.pullRancherImage(); err != nil {
		return err
	}

	// Create a container with the rancher image
	if err := r.createRancherContainer(); err != nil {
		return err
	}

	// Start the container
	if err := r.startRancherContainer(); err != nil {
		return err
	}

	// Wait for the container to be ready
	if err := r.waitForRancherContainer(); err != nil {
		return err
	}

	return nil
}

func (r *rancherDockerContainer) pullRancherImage() error {
	r.logger.Infof("Pulling rancher image %s", r.image)
	timeoutCtx, cancel := context.WithTimeout(r.ctx, requestTimeout)
	defer cancel()
	reader, err := r.dockerClient.ImagePull(timeoutCtx, r.image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	_, err = io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read image pull response: %w", err)
	}

	return reader.Close()
}

func (r *rancherDockerContainer) createRancherContainer() error {
	timeoutCtx, cancel := context.WithTimeout(r.ctx, requestTimeout)
	defer cancel()
	containerConfig := &container.Config{
		Image: r.image,
		ExposedPorts: nat.PortSet{
			containerPort:           struct{}{},
			containerPortHTTPS:      struct{}{},
			defaultK3sPort + "/tcp": struct{}{},
		},
	}

	portBindings := nat.PortMap{
		nat.Port(containerPort): []nat.PortBinding{
			nat.PortBinding{
				HostIP:   "127.0.0.1",
				HostPort: r.hostPort,
			},
		},
		nat.Port(containerPortHTTPS): []nat.PortBinding{
			nat.PortBinding{
				HostIP:   "127.0.0.1",
				HostPort: r.hostPortHTTPS,
			},
		},
		nat.Port(defaultK3sPort + "/tcp"): []nat.PortBinding{
			nat.PortBinding{
				HostIP:   "127.0.0.1",
				HostPort: defaultK3sPort,
			},
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings:  portBindings,
		Privileged:    true,
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}
	r.logger.Infof("Creating rancher container %s with image %s on host port %s and %s", r.containerName, r.image, r.hostPort, r.hostPortHTTPS)
	resp, err := r.dockerClient.ContainerCreate(timeoutCtx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return err
	}

	r.containerID = resp.ID

	return nil
}

func (r *rancherDockerContainer) startRancherContainer() error {
	r.logger.Infof("Starting rancher container %s", r.containerID)
	timeoutCtx, cancel := context.WithTimeout(r.ctx, requestTimeout)
	defer cancel()
	if err := r.dockerClient.ContainerStart(timeoutCtx, r.containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start rancher container: %w", err)
	}
	return nil
}

func (r *rancherDockerContainer) waitForRancherContainer() error {
	r.logger.Infof("Waiting for rancher container %s to be ready", r.containerID)
	pollFunc := func(ctx context.Context) (bool, error) {
		timeoutCtx, cancel := context.WithTimeout(ctx, waitInterval)
		defer cancel()
		containerJSON, err := r.dockerClient.ContainerInspect(timeoutCtx, r.containerID)
		if err != nil {
			return false, fmt.Errorf("failed to inspect container: %w", err)
		}
		if containerJSON.State.Running {
			return true, nil
		}
		r.logger.Debugf("Container %s is not yet running. State: %s", r.containerID, containerJSON.State.Status)
		return false, nil
	}

	if err := wait.PollUntilContextTimeout(r.ctx, waitInterval, waitTime, true, pollFunc); err != nil {
		return fmt.Errorf("failed to wait for rancher container: %w", err)
	}
	return nil
}

func (r *rancherDockerContainer) stop() error {
	r.logger.Infof("Stopping rancher container %s", r.containerID)
	if err := r.dockerClient.ContainerStop(r.ctx, r.containerID, container.StopOptions{}); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	r.logger.Infof("Removing rancher container %s", r.containerID)
	if err := r.dockerClient.ContainerRemove(r.ctx, r.containerID, container.RemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	return nil
}

func (r *rancherDockerContainer) getKubeConfigFromContainer() ([]byte, error) {
	r.logger.Infof("Getting kubeconfig from container %s at %s", r.containerID, kubePath)
	var reader io.ReadCloser
	var err error

	configFunc := func(context.Context) (bool, error) {
		timeoutCtx, cancel := context.WithTimeout(r.ctx, requestTimeout)
		defer cancel()
		reader, _, err = r.dockerClient.CopyFromContainer(timeoutCtx, r.containerID, kubePath)
		if err == nil {
			return true, nil
		}
		if !errdefs.IsNotFound(err) {
			return false, fmt.Errorf("failed to get kubeconfig from container: %w", err)
		}
		return false, nil
	}

	if err := wait.PollUntilContextTimeout(r.ctx, waitInterval, waitTime, true, configFunc); err != nil {
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
