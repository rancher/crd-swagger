package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/aggregator"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

const (
	kubePath       = "/etc/rancher/k3s/k3s.yaml"
	crdKind        = "CustomResourceDefinition"
	requestTimeout = time.Second * 5
	waitInterval   = time.Millisecond * 500
	waitTime       = time.Second * 15
	syncTime       = time.Second * 2
	extensionGVK   = "x-kubernetes-group-version-kind"
)

type flagVar struct {
	outputFile  string
	crdSource   string
	k3sPort     string
	prettyPrint bool
	recurse     bool
	silent      bool
}

var (
	cmdFlags     flagVar
	errDuplicate = fmt.Errorf("duplicate CRD")
)

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
	if cmdFlags.silent {
		atom.SetLevel(zapcore.FatalLevel)
		// need to set logrus level for wrangler logging
		logrus.SetLevel(logrus.FatalLevel)
	}
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
	cmd.Flags().StringVarP(&cmdFlags.crdSource, "files", "f", "", "location to find input CRD file/files, either a file path or a remote URL")
	cmd.Flags().BoolVarP(&cmdFlags.recurse, "recurse", "r", false, "if files is a directory recursively search for all CRDs")
	cmd.Flags().StringVarP(&cmdFlags.outputFile, "output-file", "o", "", "location to output the generate swagger doc (if unset stdout is used)")
	cmd.Flags().BoolVarP(&cmdFlags.prettyPrint, "pretty-print", "p", false, "print the output json with formatted with newlines and indentations")
	cmd.Flags().StringVar(&cmdFlags.k3sPort, "cluster-port", defaultK3sPort, "port to bind kubeapi-server to on the host machine")
	cmd.Flags().BoolVar(&cmdFlags.silent, "silent", false, "do not print any log messages")
	_ = cmd.MarkFlagRequired("files")
}

func run() (err error) {
	// attempt to get the desired CRDs request by the users
	zap.S().Info("Gathering CustomResourceDefinitions from source.")
	crdMap, err := crdsFromInput(cmdFlags.crdSource)
	if err != nil {
		return fmt.Errorf("failed to get CRDs: %w", err)
	}
	if len(crdMap) == 0 {
		return fmt.Errorf("no CRDs found at '%s'", cmdFlags.crdSource)
	}

	// convert the map of crds to a map of GroupKind and a list of crds to install
	desiredGroupKinds := make(map[v1.GroupKind]bool, len(crdMap))
	crdsToInstall := make([]*apiextv1.CustomResourceDefinition, 0, len(crdMap))
	for _, crd := range crdMap {
		crdsToInstall = append(crdsToInstall, crd)
		gk := v1.GroupKind{
			Group: crd.Spec.Group,
			Kind:  crd.Spec.Names.Kind,
		}
		desiredGroupKinds[gk] = true
	}

	zap.S().Info("Starting cluster in a docker container.")
	// Start the cluster for installing the CRDs and getting the swagger doc
	ctx := context.Background()
	var cluster dockerCluster
	err = cluster.start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start cluster: %w", err)
	}
	defer func() {
		stopErr := cluster.stop(ctx)
		if err == nil {
			err = stopErr
		}
	}()

	zap.S().Info("Installing CRDs into the cluster.")
	err = cluster.ensureCRD(ctx, crdsToInstall)
	if err != nil {
		return fmt.Errorf("failed to create CRDs: %w", err)
	}

	// give k8s time to add newly installed CRDs to the swagger doc
	time.Sleep(syncTime)

	zap.S().Info("Creating new Swagger doc.")
	// get the swagger doc from the crds
	swagger, err := cluster.getSwagger()
	if err != nil {
		return err
	}

	keepPaths, err := getDesiredPaths(swagger, desiredGroupKinds)
	if err != nil {
		return err
	}

	// remove all paths that are not for the desired CRDs
	aggregator.FilterSpecByPaths(swagger, keepPaths)

	err = writeDoc(swagger)
	if err != nil {
		return fmt.Errorf("failed to write swagger: %w", err)
	}

	zap.S().Info("Swagger created successfully!")
	return nil
}

// getDesiredPaths gets a list of paths to keep by checking if the path specified in the swagger doc references any of the desiredGroupKinds.
func getDesiredPaths(swagger *spec.Swagger, desiredGroupKinds map[v1.GroupKind]bool) ([]string, error) {
	if swagger.Paths == nil {
		return nil, fmt.Errorf("cluster's swagger doc has no paths set")
	}
	var keepPaths []string

	for pathName, pathItem := range swagger.Paths.Paths {
		gks := groupKindsFromPath(pathItem)
		for i := range gks {
			if desiredGroupKinds[gks[i]] {
				keepPaths = append(keepPaths, pathName)
				break
			}
		}
	}
	if keepPaths == nil {
		return nil, fmt.Errorf("failed to find any paths for CRDs.")
	}
	return keepPaths, nil
}

func writeDoc(swagger *spec.Swagger) error {
	var outData []byte
	var err error
	if cmdFlags.prettyPrint {
		outData, err = json.MarshalIndent(swagger, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal swagger: %w", err)
		}
	} else {
		outData, err = json.Marshal(swagger)
		if err != nil {
			return fmt.Errorf("failed to marshal swagger: %w", err)
		}
	}
	if cmdFlags.outputFile == "" {
		outData = append(outData, '\n')
		_, err := os.Stdout.Write(outData)
		if err != nil {
			return fmt.Errorf("failed to write swagger to stdout: %w", err)
		}
		return nil
	}
	err = os.WriteFile(cmdFlags.outputFile, outData, 0600)
	if err != nil {
		return fmt.Errorf("failed to write swagger doc: %w", err)
	}
	return nil
}
