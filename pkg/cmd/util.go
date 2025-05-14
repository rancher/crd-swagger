package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

const extensionGVK = "x-kubernetes-group-version-kind"

func getContentReader(source string, logger *zap.SugaredLogger) (io.ReadCloser, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		logger.Infof("Fetching resources from URL: %s", source)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
		if err != nil {
			logger.Errorf("Failed to create HTTP request for URL '%s': %v", source, err)
			return nil, fmt.Errorf("failed to create request for URL '%s': %w", source, err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Errorf("Failed to fetch content from URL '%s': %v", source, err)
			return nil, fmt.Errorf("failed to fetch from URL '%s': %w", source, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			logger.Errorf("Failed to fetch content from URL '%s': status code %d", source, resp.StatusCode)
			return nil, fmt.Errorf("bad status fetching from URL '%s': %s", source, resp.Status)
		}
		return resp.Body, nil
	}

	logger.Infof("Reading resources from local file: %s", source)
	file, err := os.Open(source)
	if err != nil {
		logger.Errorf("Failed to open local resources file '%s': %v", source, err)
		return nil, fmt.Errorf("failed to open local file '%s': %w", source, err)
	}
	return file, nil // os.File is an io.ReadCloser
}

func parseGroupKind(filePath string, logger *zap.SugaredLogger) (map[metav1.GroupKind]bool, error) {
	logger.Info("Parsing resources file: ", filePath)
	contentReader, err := getContentReader(filePath, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get content reader for '%s': %w", filePath, err)
	}
	defer contentReader.Close()

	groupKindsMap := make(map[metav1.GroupKind]bool)
	scanner := bufio.NewScanner(contentReader)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ".", 2)
		var gk metav1.GroupKind
		if len(parts) == 2 {
			gk = metav1.GroupKind{Group: parts[1], Kind: parts[0]}
		} else {
			gk.Kind = parts[0]
		}
		groupKindsMap[gk] = false
		logger.Infof("Resource %s parsed successfully", gk.String())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file '%s': %w", filePath, err)
	}
	if len(groupKindsMap) == 0 {
		return nil, fmt.Errorf("no GroupKind found in file '%s'", filePath)
	}
	return groupKindsMap, nil
}

func getDesiredPaths(swagger *spec.Swagger, desiredGroupKinds map[metav1.GroupKind]bool, logger *zap.SugaredLogger) ([]string, error) {
	logger.Info("Getting desired paths from swagger doc")
	if swagger.Paths == nil {
		return nil, fmt.Errorf("cluster's swagger doc has no paths set")
	}
	var keepPaths []string
	for pathName, pathItem := range swagger.Paths.Paths {
		gks := groupKindsFromPath(pathItem, logger)
		for i := range gks {
			if _, ok := desiredGroupKinds[gks[i]]; ok {
				keepPaths = append(keepPaths, pathName)
				desiredGroupKinds[gks[i]] = true // set the GK as found
				break
			}
		}
	}
	for gk, foundPath := range desiredGroupKinds {
		if !foundPath {
			return nil, fmt.Errorf("failed to find path for GroupKind %s", gk.String())
		}
	}
	return keepPaths, nil
}

func writeDoc(swagger *spec.Swagger, logger *zap.SugaredLogger) error {
	logger.Info("Writing swagger doc")
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

func groupKindsFromPath(path spec.PathItem, logger *zap.SugaredLogger) []metav1.GroupKind {
	gks := map[metav1.GroupKind]bool{}
	ops := map[string]*spec.Operation{
		"GET": path.Get, "PUT": path.Put, "POST": path.Post, "DELETE": path.Delete,
		"OPTIONS": path.Options, "HEAD": path.Head, "PATCH": path.Patch,
	}
	for opName, op := range ops {
		err := addGKFromOp(op, gks)
		if err != nil {
			logger.Infof("Failed to get GroupKind for Path %s.%s : %v", path, opName, err)
		}
	}

	retSlice := make([]metav1.GroupKind, 0, len(gks))
	for gk := range gks {
		retSlice = append(retSlice, gk)
	}
	return retSlice
}

func addGKFromOp(operation *spec.Operation, gks map[metav1.GroupKind]bool) error {
	if operation == nil {
		return nil
	}
	var newGKs metav1.GroupKind
	err := operation.Extensions.GetObject(extensionGVK, &newGKs)
	if err != nil {
		return fmt.Errorf("failed to get gk from operation: %w", err)
	}

	gks[newGKs] = true
	return nil
}
