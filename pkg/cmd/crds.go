package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rancher/wrangler/v2/pkg/yaml"
	"go.uber.org/zap"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func groupKindsFromPath(path spec.PathItem) []v1.GroupKind {
	gks := map[v1.GroupKind]bool{}

	err := addGKFromOp(path.Get, gks)
	if err != nil {
		zap.S().Infof("Get: %v", err)
	}

	err = addGKFromOp(path.Put, gks)
	if err != nil {
		zap.S().Infof("Put: %v", err)
	}

	err = addGKFromOp(path.Post, gks)
	if err != nil {
		zap.S().Infof("Post: %v", err)
	}

	err = addGKFromOp(path.Delete, gks)
	if err != nil {
		zap.S().Infof("Delete: %v", err)
	}

	err = addGKFromOp(path.Options, gks)
	if err != nil {
		zap.S().Infof("Options: %v", err)
	}

	err = addGKFromOp(path.Head, gks)
	if err != nil {
		zap.S().Infof("Head: %v", err)
	}

	err = addGKFromOp(path.Patch, gks)
	if err != nil {
		zap.S().Infof("Patch: %v", err)
	}
	retSlice := make([]v1.GroupKind, 0, len(gks))
	for gk := range gks {
		retSlice = append(retSlice, gk)
	}
	return retSlice
}

func addGKFromOp(operation *spec.Operation, gks map[v1.GroupKind]bool) error {
	if operation == nil {
		return nil
	}
	var newGKs v1.GroupKind
	err := operation.Extensions.GetObject(extensionGVK, &newGKs)
	if err != nil {
		return fmt.Errorf("failed to get gk from operation: %w", err)
	}

	gks[newGKs] = true
	return nil
}

func crdsFromInput(path string) (map[string]*apiextv1.CustomResourceDefinition, error) {
	allCRDs := map[string]*apiextv1.CustomResourceDefinition{}

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return allCRDs, crdsFromURL(path, allCRDs)
	}
	statInfo, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file '%s': %w", path, err)
	}
	if !statInfo.IsDir() {
		return allCRDs, crdFromFile(path, allCRDs)
	}
	return allCRDs, crdsFromDir(path, allCRDs)
}

// crdsFromDir recursively traverses the embedded yaml directory and find all CRD yamls.
func crdsFromDir(dirName string,
	allCRDs map[string]*apiextv1.CustomResourceDefinition) error {

	// read all entries in the directory
	crdFiles, err := os.ReadDir(dirName)
	if err != nil {
		return fmt.Errorf("failed to read dir '%s': %w", dirName, err)
	}

	for _, dirEntry := range crdFiles {
		fullPath := filepath.Join(dirName, dirEntry.Name())
		if dirEntry.IsDir() && cmdFlags.recurse {
			// if the entry is the dir recurse into that folder to get all crds
			err := crdsFromDir(fullPath, allCRDs)
			if err != nil {
				return err
			}
			continue
		}
		crdFromFile(fullPath, allCRDs)
	}
	return nil
}

func crdFromFile(fullPath string, allCRDs map[string]*apiextv1.CustomResourceDefinition) error {
	// read the file and convert it to a crd object
	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("failed to open file '%s': %w", fullPath, err)
	}
	defer file.Close()
	err = crdFromReader(file, allCRDs)
	if err != nil {
		return fmt.Errorf("failed to convert file '%s': %w", fullPath, err)
	}
	return nil
}

func crdFromReader(reader io.Reader, allCRDs map[string]*apiextv1.CustomResourceDefinition) error {
	crdObjs, err := yaml.UnmarshalWithJSONDecoder[*apiextv1.CustomResourceDefinition](reader)
	if err != nil {
		return fmt.Errorf("failed decode yaml: %w", err)
	}
	for _, crdObj := range crdObjs {
		if crdObj.Kind != crdKind {
			// if the yaml is not a CRD skip it
			continue
		}
		if _, ok := allCRDs[crdObj.Name]; ok {
			return fmt.Errorf("%w for '%s", errDuplicate, crdObj.Name)
		}
		allCRDs[crdObj.Name] = crdObj
	}
	return nil
}

func crdsFromURL(url string, allCRDs map[string]*apiextv1.CustomResourceDefinition) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to get request YAML: %w", err)
	}
	defer resp.Body.Close()
	err = crdFromReader(resp.Body, allCRDs)
	if err != nil {
		return fmt.Errorf("failed to convert response: %w", err)
	}
	return nil
}
