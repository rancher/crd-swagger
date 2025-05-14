package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/KevinJoiner/crd-swagger/pkg/aggregator"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type commandFlags struct {
	resourcesFile string
	outputFile    string
	prettyPrint   bool

	rancherVersion string
	hostPortHTTP   string
	hostPortHTTPS  string

	rancherDevImage string
}

var cmdFlags commandFlags

// NewRootCommand returns the root crd-swagger command.
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crd-swagger",
		Short: "crd-swagger creates swagger docs for CRDs",
		Long:  `Generates a Swagger (openapiv2) document for Custom Resource Definitions (CRDs) installed and accessed through kube-apiserver.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setupLogger(); err != nil {
				return err
			}
			defer zap.L().Sync()
			return run()
		},
	}
	addFlags(cmd)
	return cmd
}

func setupLogger() error {
	atom := zap.NewAtomicLevel()
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	logger := zap.New(zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.Lock(os.Stdout),
		atom,
	))
	_ = zap.ReplaceGlobals(logger)
	return nil
}

func addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&cmdFlags.resourcesFile, "resources-file", "f", "", "Path to a file containing Kind.Group resources (e.g., RoleTemplate.management.cattle.io), one per line")
	cmd.Flags().StringVarP(&cmdFlags.outputFile, "output-file", "o", "", "Output file for the generated OpenAPI (Swagger) document (default: stdout)")
	cmd.Flags().BoolVarP(&cmdFlags.prettyPrint, "pretty-print", "j", false, "Pretty-print the output JSON with indentation")

	cmd.Flags().StringVarP(&cmdFlags.rancherVersion, "rancher-version", "v", "", "Rancher Docker image version (e.g., v2.8.3) default: latest)")
	cmd.Flags().StringVarP(&cmdFlags.rancherDevImage, "rancher-dev-image", "i", "", "Custom/Dev Rancher Docker image (e.g., exampleRepository/rancher:dev)")

	cmd.Flags().StringVarP(&cmdFlags.hostPortHTTP, "http-port", "p", defaultHostPort, "Host port for Rancher HTTP traffic (e.g., 80, 8080)")
	cmd.Flags().StringVarP(&cmdFlags.hostPortHTTPS, "https-port", "t", defaultHostPortHTTPS, "Host port for Rancher HTTPS traffic (e.g. tls port: 443, 8443)")

	if err := cmd.MarkFlagRequired("resources-file"); err != nil {
		panic(err)
	}
}

func run() (err error) {
	logger := zap.S()

	if cmdFlags.rancherDevImage != "" && cmdFlags.rancherVersion != "" {
		return fmt.Errorf("cannot specify both --rancher-dev-image and --rancher-version flags at the same time")
	}

	desiredGroupKinds, err := parseGroupKind(cmdFlags.resourcesFile, logger)
	if err != nil {
		return fmt.Errorf("failed to split group kind: %w", err)
	}

	logger.Info("Initializing Rancher Docker container...")
	ctx := context.Background()
	rancherContainer, err := newRancherDockerContainer(
		ctx,
		logger,
		cmdFlags.rancherDevImage,
		cmdFlags.rancherVersion,
		cmdFlags.hostPortHTTP,
		cmdFlags.hostPortHTTPS,
	)
	if err != nil {
		return fmt.Errorf("failed to create rancher docker container: %w", err)
	}

	logger.Infof("Rancher Docker container %s initialized with image: %s", rancherContainer.containerName, rancherContainer.image)

	err = rancherContainer.start()
	if err != nil {

		return fmt.Errorf("failed to start rancher container: %w", err)
	}
	logger.Info("Rancher container started successfully", "containerID", rancherContainer.containerID)

	defer func() {
		logger.Info("Attempting to stop and remove Rancher container...")
		stopErr := rancherContainer.stop()
		if err == nil {
			err = stopErr
		}
	}()

	logger.Info("Fetching kubeconfig from container...")
	kubeConfig, err := rancherContainer.getKubeConfigFromContainer()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig from container: %w", err)
	}

	logger.Info("Initializing Kubernetes client and fetching OpenAPI spec...")

	kubeClient, err := newKubeClient(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed to create kube client: %w", err)
	}

	logger.Info("Waiting for desired resources to be available...")
	err = kubeClient.waitForDesiredResources(ctx, desiredGroupKinds, logger)
	if err != nil {
		return fmt.Errorf("failed to wait for desired resources: %w", err)
	}
	logger.Info("Desired resources are available")

	logger.Info("Fetching OpenAPI spec from cluster...")
	swagger, err := kubeClient.getSwagger()
	if err != nil {
		return fmt.Errorf("failed to get swagger from cluster: %w", err)
	}
	if swagger == nil {
		return fmt.Errorf("cluster's swagger doc is nil")
	}

	logger.Info("Getting desired paths from swagger spec...")
	keepPaths, err := getDesiredPaths(swagger, desiredGroupKinds, logger)
	if err != nil {
		return err
	}

	logger.Info("Filtering swagger spec by desired paths...")
	aggregator.FilterSpecByPaths(swagger, keepPaths)

	logger.Infof("Writing filtered swagger spec to output file '%s'", cmdFlags.outputFile)
	err = writeDoc(swagger, logger)
	if err != nil {
		return fmt.Errorf("failed to write swagger: %w", err)
	}
	logger.Infof("Filtered swagger spec written to '%s'", cmdFlags.outputFile)
	logger.Info("OpenAPI (Swagger) document generated successfully!")
	return nil
}
