Here’s a sample `README.md` you can place in your vendored/copied `aggregator` package to document the situation, the patch, and the rationale:

---

# aggregator (copied from kube-openapi)

This package is a direct copy of the upstream [`k8s.io/kube-openapi/pkg/aggregator`](https://github.com/kubernetes/kube-openapi/tree/master/pkg/aggregator) as of [commit XXXXXX](https://github.com/kubernetes/kube-openapi/commit/XXXXXX).

## Why is this copied?

We maintain a local copy to apply a custom patch that improves reference ($ref) walking for OpenAPI parameter objects.  
This is necessary to ensure all referenced definitions (such as `DeleteOptions`) are preserved during filtering, fixing issues with broken `$ref` links in the generated Swagger spec.

For context, see this [comment](https://github.com/rancher/crd-swagger/pull/2#issuecomment-2904590806).


The key patch walker.go, `walkOnAllReferences` function:
```go
 if refStr == "" {
    return
}
if strings.HasPrefix(refStr, parameterPrefix) {
    paramName := refStr[len(parameterPrefix):]
    param := root.Parameters[paramName]
    walker.walkRefCallback(&param.Ref)
    walker.walkSchema(param.Schema)
    if param.Items != nil {
        walker.walkRefCallback(&param.Items.Ref)
    }
}

if strings.HasPrefix(refStr, definitionPrefix) {
    defName := refStr[len(definitionPrefix):]

    if _, found := root.Definitions[defName]; found && !alreadyVisited[refStr] {
        alreadyVisited[refStr] = true
        def := root.Definitions[defName]
        walker.walkSchema(&def)
    }
}
```
  This ensures all referenced schemas are preserved, even if only referenced by parameters.

## Upstream

If you update this package from upstream, please re-apply the patch or upstream the fix.
