# Deployment Validation Operator

## Description

The Deployment Validation Operator (DVO) checks deployments and other resources against a curated collection of best practices. 

These best practices focus mainly on ensuring that the applications are fault-tolerant.

DVO will only monitor Kubernetes resources and will not modify them in any way. As an operator it is a continuously running version of the static analysis tool Kube-linter [https://github.com/stackrox/kube-linter]. It will report failed validations via Prometheus, which will allow users of this operator to create alerts based on its results. All the metrics are gauges that will report `1` if the best-practice has failed. The metric will always have three parameters: `name`, `namespace` and `kind`. 

This operator doesn't define any CRDs at the moment. It has been bootstrapped with `operator-sdk` making it possible to add a CRD in the future if required.

## Architecture Diagrams

[Architecure Diagrams](./docs/architecture.md)

## Running the operator locally

To build the operator binary, you can run the following make target:

```
make go-build
```

The binary is created in the `build/_output/bin/` directory and can be run using:

```
POD_NAMESPACE="deployment-validation-operator" WATCH_NAMESPACE="" NAMESPACE_IGNORE_PATTERN='^(openshift.*|kube-.*)$' build/_output/bin/deployment-validation-operator --kubeconfig=$HOME/.kube/config --zap-devel
```

Finally you can check metrics exposed by the operator with:

```
curl localhost:8383/metrics
```

## Deployment

The manifests to deploy DVO take a permissive approach to permissions.  This is done to make it easier to support monitoring new object kinds without having to change rbac rules.  This means that elevated permissions will be required in order to deploy DVO through standard manifests.  There is a manifest to deploy DVO though OLM from opereatorhub which does alleviate this need to have elevated permissions.

* DVO deployment should only deploy 1 pod as currently metrics are not replicated across a standard 3 causing installation issues (will be fixed in a later version)

### Manual installation

There are manifests to install the operator under the [`deploy/openshift`](deploy/openshift) directory. A typical installation would go as follows:

* Create the `deployment-validation-operator` namespace/project
    * If deploying to a namespace other than `deployment-validation-operator`, there are commented lines you must change in `deploy/openshift/cluster-role-binding.yaml` and `deploy/openshift/role-binding.yaml` first
* Create the service, service account, configmap, roles and role bindings
* Create the operator deployment
    * **Note that the `nodeAffinity` attribute by default requires a node with the `node-role.kubernetes.io/infra` selector. In common (self-managed) clusters there is usually no such node, so you can remove the `nodeAffinity` attribute when deploying to those environments.**

```
oc new-project deployment-validation-operator
for manifest in service-account.yaml \
                service.yaml \
                role.yaml \
                cluster-role.yaml \
                role-binding.yaml \
                cluster-role-binding.yaml \
                configmap.yaml \
                operator.yaml
do
    oc create -f deploy/openshift/$manifest
done
```
## Install Grafana dashboard

There are manifests to install a simple grafana dashboard under the [`deploy/observability`](deploy/observability) directory.

A typical installation to the default namespace `deployment-validation-operator` goes as follows:
`oc process -f deploy/observability/template.yaml | oc create -f -`

Or, if you want to deploy deployment-validation-operator components to a custom namespace:
`oc process --local NAMESPACE="custom-dvo-namespace" -f deploy/observability/template.yaml | oc create -f -`

## Allow scraping from outside DVO namespace

The metrics generated by DVO can be scraped by anything that understands prometheus metrics.  A network policy may be needed to allow the DVO metrics to be collected from a service running in a namespace other than the one where DVO is deployed.  For example, if a service in `some-namespace` wants to scrape the metrics from DVO then a network policy would need to be created like this:

```
oc process --local NAMESPACE='some-namespace' -f deploy/openshift/network-policies.yaml | oc create -f -
```
## Excluding resources from operator validation

There are two options to exclude the cluster resources from operator validation:

* exclude the whole namespace by creating (or updating) the `NAMESPACE_IGNORE_PATTERN` environment variable
* exclude a resource by using corresponding kube-linter annotation - see [Ignore specific resources](#ignore-specific-resources)

## Configuring Checks

DVO performs validation checks using kube-linter. The checks configuration is mirrored to the one for the kube-linter project. More information on configuration options can be found [here](https://github.com/stackrox/kube-linter/blob/main/docs/configuring-kubelinter.md), and a list of available checks  can be found [here](https://github.com/stackrox/kube-linter/blob/main/docs/generated/checks.md).

To configure DVO with a different set of checks, create a ConfigMap in the cluster with the new checks configuration. An example of a configuration ConfigMap can be found [here](./deploy/openshift/configmap.yaml).

If no custom configuration is found (the ConfigMap does not exist or does not contain a check declaration), the operator enables the following checks by default:
* "host-ipc"
* "host-network"
* "host-pid"
* "non-isolated-pod"
* "pdb-max-unavailable"
* "pdb-min-available"
* "privilege-escalation-container"
* "privileged-container"
* "run-as-non-root"
* "unsafe-sysctls"
* "unset-cpu-requirements"
* "unset-memory-requirements"

### Enabling checks

To enable all checks, set the `addAllBuiltIn` property to `true`. If you only want to enable individual checks, include them as a collection in the `include` property and leave `addAllBuiltIn` with a value of `false`.

The `include` property can work together with `doNotAutoAddDefaults` set to `true` in a whitelisting way. Only the checks collection passed in `include` will be executed.

### Disabling checks

To disable all checks, set the `doNotAutoAddDefaults` property to `true`. If you only want to disable individual checks, include them as a collection in the `exclude` property and leave `doNotAutoAddDefaults` with a value of `false`

The `exclude` property takes precedence over the `include` property. If a particular check is in both collections, it will be excluded by default.

The `exclude` property can work in conjunction with `addAllBuiltIn` set to `true` in a blacklisting fashion. All checks will be triggered and only the checks passed in `exclude` will be ignored.

#### Ignore specific resources

It is possible to exclude certain resources from any or all validations. This is achieved by adding annotations to the resources we want DVO to ignore.

To ignore a specific check, the annotation will have a key like `ignore-check.kube-linter.io/check-name`. Where `check-name` can be any supported or custom check. It is recommended that the value for this annotation is a clear explanation of why the resource should be ignored.

To ignore all checks, the annotation key is `kube-linter.io/ignore-all`. Again, it is recommended to include a meaningful explanation in the value of the annotation.

e.g. ignoring **run-as-non-root** check
```yaml
metadata:
  annotations:
    ignore-check.kube-linter.io/run-as-non-root: "This image must be run as a privileged user for it to work."
```
e.g. ignoring all checks
```yaml
metadata:
  annotations:
    kube-linter.io/ignore-all: "This deployment is managed by an OLM subscription"
```

This feature is maintained by kube-linter, [more info](https://docs.kubelinter.io/#/configuring-kubelinter?id=ignoring-violations-for-specific-cases)

## Tests

You can run the unit tests via

```
make test
```

The end-to-end tests depend on [`ginkgo`](https://onsi.github.io/ginkgo/#installing-ginkgo). After exporting a `KUBECONFIG` variable, it can be run via

```
make e2e-test
```

The OCP e2e PR checks exist in the [deployment-validation-operator-tests](https://gitlab.cee.redhat.com/ccx/deployment-validation-operator-tests) repository.
Tests are developed there and once a new build is done, the image is pushed onto [quay.io](https://quay.io/repository/redhatqe/deployment-validation-operator-tests).
This image is then mirrored by the mirroring job in openshift release with this [config](https://github.com/openshift/release/blob/master/core-services/image-mirroring/supplemental-ci-images/mapping_supplemental_ci_images_ci#L22).
The config file for the e2e tests job is then found [here](https://github.com/openshift/release/blob/master/ci-operator/config/app-sre/deployment-validation-operator/app-sre-deployment-validation-operator-master.yaml).

Since these tests depend on the content of the deploy/openshift folder, if any changes are done there, please run the following command:
```
operator-sdk generate bundle --package=deployment-validation-operator --input-dir=deploy/openshift
```
It is then necessary to head to bundle/manifests/clusterserviceversion.yaml and search for and remove the NodeAffinity section.

## Releases

To create a new DVO release follow this [New DVO Release](./docs/new-releases.md)

## Roadmap

- e2e tests
