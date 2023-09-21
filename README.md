# crd-swagger
Generates a [Swagger](https://swagger.io/docs/) (openapiv2) document for [Custom Resource Definitions](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/) (CRDs) installed and accessed through kube-apiserver.
The generated swagger is the same document that you would get from the [openapiv2 endpoint](https://kubernetes.io/docs/concepts/overview/kubernetes-api/#openapi-v2) on a cluster, except the returned swagger document only includes paths and definitions used by the provided CRDs.

The application performs the following steps to accomplish this
1. Gets the requested CRD files either from a local or remote file location.
2. Starts a new K3s docker container.
3. Creates the requested CRDs in the new cluster
4. Retrieves a swagger 2.0 doc from the cluster.
5. Removes all paths and references not related to the requested CRDs
6. Outputs the new filtered swagger.json

## Installation
``` bash
go install github.com/kevinjoiner/crd-swagger
```

### Requirements
- golang
- docker

## Usage
```
Generates a Swagger (openapiv2) document for Custom Resource Definitions (CRDs) installed and accessed through kube-apiserver.

Usage:
  crd-swagger [flags]

Flags:
      --cluster-port string   port to bind kubeapi-server to on the host machine (default "6443")
  -f, --files string          location to find input CRD file/files, either a file path or a remote URL
  -h, --help                  help for crd-swagger
  -o, --output-file string    location to output the generate swagger doc (if unset stdout is used)
  -p, --pretty-print          print the output json with formatted with newlines and indentations
  -r, --recurse               if files is a directory recursively search for all CRDs
      --silent                do not print any log messages
  ```
## Example
Generate swagger.json from a local Yaml file with readable new lines and indents
```
crd-swagger -o swagger.json -p -f ./crds.yaml
```
Generate rancher-swagger.json from remote gist
```
crd-swagger -o rancher-swagger.json -f https://gist.githubusercontent.com/KevinJoiner/088b29a495c3043fd59d8673ef6c7c05/raw
```
