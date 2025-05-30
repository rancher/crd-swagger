# crd-swagger

Generates a filtered OpenAPI (Swagger) v2 document for specified Kubernetes resources. This tool interacts with a live, temporary Kubernetes API server (K3s running in Docker via a Rancher image) to achieve this.

The primary use case is to generate precise OpenAPI specifications for Rancher's public APIs.

## How It Works

The application performs the following steps:

1. **Read Target Resources**  
    Parses a list of target resources from an input file provided via `--resources-file`. The file (local or URL-based) should list resources in the `Kind.Group` format (e.g., `RoleTemplate.management.cattle.io`).

2. **Start Rancher Docker Instance**  
    Launches a Rancher Docker container using the specified Rancher image version.

3. **Fetch Kubeconfig from Container**  
    Retrieves the Kubeconfig for the K3s cluster running inside the Rancher container.

4. **Kubernetes API Discovery**  
    Connects to the K3s cluster and uses the Kubernetes Discovery API to identify available resources.

5. **Fetch Full OpenAPI Spec**  
    Retrieves the complete OpenAPI v2 document from the K3s API server.

6. **Filter OpenAPI Spec**  
    Filters the OpenAPI document to retain only the API paths and schema definitions that match the target resources identified via discovery.

7.  **Output:** 
    Writes the filtered `swagger.json` (OpenAPI v2) document to standard output or a specified output file.

## Installation

```bash
go install github.com/rancher/crd-swagger.git
```


### Requirements

*   Go (for installation and development)
*   Docker (for running the Rancher)

## Usage

```
Generates a Swagger (openapiv2) document for Custom Resource Definitions (CRDs) installed and accessed through kube-apiserver.

Usage:
  crd-swagger [flags]

Flags:
  -h, --help                     help for crd-swagger
  -p, --http-port string         Host port for Rancher HTTP traffic (e.g., 80, 8080) (default "80")
  -t, --https-port string        Host port for Rancher HTTPS traffic (e.g. tls port: 443, 8443) (default "443")
  -o, --output-file string       Output file for the generated OpenAPI (Swagger) document (default: stdout)
  -j, --pretty-print             Pretty-print the output JSON with indentation
  -v, --rancher-version string   Rancher Docker image version (e.g., v2.8.3, latest) (default "latest")
  -f, --resources-file string    Path to a file containing Kind.Group resources (e.g., RoleTemplate.management.cattle.io), one per line
```

## Examples

### 1. Basic Usage with a Local Resources File

First, create a file (e.g., `my-apis.txt`) with the `Kind.Group` of the resources you want to document:

**`my-apis.txt`:**
```
GlobalRole.management.cattle.io
GlobalRoleBinding.management.cattle.io
RoleTemplate.management.cattle.io
ProjectRoleTemplateBinding.management.cattle.io
ClusterRoleTemplateBinding.management.cattle.io
Project.management.cattle.io
```

Then, run the tool:

```bash
crd-swagger -f my-apis.txt -o filtered-swagger.json
```
This will:
*   Read `my-apis.txt`.
*   Start a K3s cluster using `rancher/rancher:v2.8.3`.
*   Generate `filtered-swagger.json` with pretty-printing.

### 2. Using a Remote URL for the Resources File

If your list of `Kind.Group`s is hosted online:

```bash
crd-swagger -f https://gist.githubusercontent.com/pratikjagrut/46d5e386daed88cfd9ab3b72600e8b8d/raw/340d317eed8a712cbb9a11e6e09f8a9c6e0d0617/resources -o remote-filtered.json
```

