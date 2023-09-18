package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/rancher/wrangler/v2/pkg/crd"
	"github.com/spf13/cobra"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kube-openapi/pkg/aggregator"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

const (
	kubePath       = "/etc/rancher/k3s/k3s.yaml"
	crdKind        = "CustomResourceDefinition"
	requestTimeout = time.Second * 5
	waitInterval   = time.Millisecond * 500
	waitTime       = time.Second * 15
	extensionGVK   = "x-kubernetes-group-version-kind"
)

type flagVar struct {
	prettyPrint bool
	recurse     bool
	outputFile  string
	inputLoc    string
	k3sPort     string
}

var (
	cmdFlags     flagVar
	errDuplicate = fmt.Errorf("duplicate CRD")
)

// NewRootCommand returns the root crd-swagger command
func NewRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crd-swagger",
		Short: "crd-swagger creates swagger docs for CRDs",
		Long:  `Generates a Swagger (openapiv2) document for Custom Resource Definitions (CRDs) installed and accessed through kube-apiserver.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}
	addFlags(cmd)
	return cmd
}

func addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&cmdFlags.inputLoc, "files", "f", "", "location to find input CRD file/files, either a file path or a remote URL")
	cmd.Flags().BoolVarP(&cmdFlags.recurse, "recurse", "r", false, "if files is a directory recursively search for all CRDs")
	cmd.Flags().StringVarP(&cmdFlags.outputFile, "output-file", "o", "", "location to output the generate swagger doc (if unset stdout is used)")
	cmd.Flags().BoolVarP(&cmdFlags.prettyPrint, "pretty-print", "p", false, "print the output json with formatted with newlines and indentations")
	cmd.Flags().StringVar(&cmdFlags.k3sPort, "cluster-port", defaultK3sPort, "port to bind kubeapi-server to on the host machine")
	_ = cmd.MarkFlagRequired("files")
}

func run() (err error) {
	crdMap, err := crdsFromInput(cmdFlags.inputLoc)
	if err != nil {
		return fmt.Errorf("failed to get CRDs: %w", err)
	}
	if len(crdMap) == 0 {
		return fmt.Errorf("no CRDs found at '%s'", cmdFlags.inputLoc)
	}

	// convert the map of crds to a map of GroupKind and a list of crds to install
	crdGKs := make(map[v1.GroupKind]bool, len(crdMap))
	crds := make([]*apiextv1.CustomResourceDefinition, 0, len(crdMap))
	for _, crd := range crdMap {
		crds = append(crds, crd)
		gk := v1.GroupKind{
			Group: crd.Spec.Group,
			Kind:  crd.Spec.Names.Kind,
		}
		crdGKs[gk] = true
	}

	// Start the cluster for installing the CRDs and getting the swagger doc
	ctx := context.Background()
	cluster := dockerCluster{}
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

	// install the CRDs
	clientSet := cluster.ClientSet()
	err = crd.BatchCreateCRDs(ctx, clientSet.ApiextensionsV1().CustomResourceDefinitions(), labels.Everything(), waitTime, crds)
	if err != nil {
		return fmt.Errorf("failed to create CRDs: %w", err)
	}

	// give k8s time to add newly installed CRDs to the swagger doc
	time.Sleep(time.Second * 2)

	// get the swagger doc from the crds
	protoSwagger, err := clientSet.Discovery().OpenAPISchema()
	if err != nil {
		return fmt.Errorf("failed to get swagger from cluster: %w", err)
	}
	var swagger spec.Swagger
	ok, err := swagger.FromGnostic(protoSwagger)
	if err != nil || !ok {
		return fmt.Errorf("failed to convert protoSwagger struct: %w", err)
	}

	keepPaths := []string{}
	if swagger.Paths == nil {
		return fmt.Errorf("returned swagger from cluster has no paths set")
	}

	// get a list of paths with GroupKinds that we care about.
	for pathName, pathItem := range swagger.Paths.Paths {
		gks := gkFromPath(pathItem)
		for i := range gks {
			if crdGKs[gks[i]] {
				keepPaths = append(keepPaths, pathName)
				break
			}
		}
	}

	// remove all paths that are not for the desired CRDs
	aggregator.FilterSpecByPaths(&swagger, keepPaths)

	err = writeDoc(&swagger)
	if err != nil {
		return fmt.Errorf("failed to write swagger: %w", err)
	}

	return nil
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
	err = os.WriteFile(cmdFlags.outputFile, outData, 0666)
	if err != nil {
		return fmt.Errorf("failed to write swagger doc: %w", err)
	}
	return nil
}
