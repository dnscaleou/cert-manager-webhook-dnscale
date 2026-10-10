# DNScale DNS-01 webhook for cert-manager

This webhook lets cert-manager create and remove ACME DNS-01 TXT records through
the DNScale API. It supports wildcard certificates, namespaced `Issuer` resources,
and `ClusterIssuer` resources.

- Webhook group: `acme.dnscale.eu`
- Solver name: `dnscale`
- API: `https://api.dnscale.eu`
- Challenge TXT TTL: 300 seconds

Versioned releases include a public Linux AMD64/ARM64 container image, an OCI
Helm chart, and a chart archive with checksums on
[GitHub Releases](https://github.com/dnscaleou/cert-manager-webhook-dnscale/releases).
The image version is `v1.0.0`; the Helm chart version is `1.0.0`.

## Install

Prerequisites: a Kubernetes cluster with cert-manager installed, Helm 3.8+, and a
DNScale-hosted zone publicly delegated to its assigned nameservers. Merely
creating a zone in the dashboard does not make it publicly resolvable.

The installation matrix uses Kubernetes 1.35.0 with cert-manager 1.20.4 and
1.21.2. Source builds require Go 1.26. The Go dependencies match cert-manager
1.21.2's Kubernetes libraries; keep those aligned when upgrading, because
mismatched versions can compile but fail on startup.

```sh
helm install cert-manager-webhook-dnscale \
  oci://ghcr.io/dnscaleou/charts/cert-manager-webhook-dnscale \
  --version 1.0.0 --namespace cert-manager \
  --set fullnameOverride=cert-manager-webhook-dnscale \
  --wait --timeout 180s

kubectl wait apiservice/v1alpha1.acme.dnscale.eu \
  --for=condition=Available --timeout=60s
```

The chart uses `ghcr.io/dnscaleou/cert-manager-webhook-dnscale:v1.0.0` by default.
Both packages can be pulled without registry credentials. The release notes
include the immutable image digest, and the attached `SHA256SUMS` verifies the
downloadable chart archive.

### Build from source

For a custom build or a local development cluster:

```sh
git clone --branch v1.0.0 https://github.com/dnscaleou/cert-manager-webhook-dnscale.git
cd cert-manager-webhook-dnscale

# Choose a registry/repository that your cluster can pull from.
IMAGE_REPOSITORY=registry.example.com/your-team/cert-manager-webhook-dnscale
IMAGE_TAG=v1.0.0
docker build -t "$IMAGE_REPOSITORY:$IMAGE_TAG" .
docker push "$IMAGE_REPOSITORY:$IMAGE_TAG"

helm install cert-manager-webhook-dnscale ./deploy/dnscale-webhook \
  --namespace cert-manager \
  --set fullnameOverride=cert-manager-webhook-dnscale \
  --set image.repository="$IMAGE_REPOSITORY" \
  --set image.tag="$IMAGE_TAG" \
  --wait --timeout 180s

kubectl wait apiservice/v1alpha1.acme.dnscale.eu \
  --for=condition=Available --timeout=60s
```

Build for the architecture used by your cluster. For a disposable kind cluster,
load the image with `kind load docker-image` instead of pushing it, and set
`image.pullPolicy=Never` when installing the chart.

## Credentials and permissions

Create a DNScale API token with `zones:read`, `records:read`, and `records:write`,
limited to the validation zone and exact challenge owners you need. For example,
`_acme-challenge` covers an apex certificate and its wildcard;
`_acme-challenge.app` covers `app.example.com`.

Save only the token in a local `.test-api-token` file, restrict its permissions,
and create the Secret without putting the token in shell history:

```sh
chmod 600 .test-api-token
kubectl -n cert-manager create secret generic dnscale-api-token \
  --from-file=api-token=.test-api-token
```

The file is excluded from Git and Docker build contexts. Do not commit tokens or
Secret manifests containing credentials.

For a `ClusterIssuer`, cert-manager supplies its cluster resource namespace,
normally `cert-manager`, as the credential namespace. For an `Issuer`, the Secret
must be in that Issuer's namespace. The webhook uses the namespace supplied by
cert-manager; it does not accept an arbitrary Secret namespace in solver config.

The chart grants `get` permission only on named Secrets. To support an Issuer in
`apps`, create the namespace and Secret, then add this entry to a values file:

```yaml
secretAccess:
  - namespace: cert-manager
    secretNames: [dnscale-api-token]
  - namespace: apps
    secretNames: [dnscale-api-token]
```

Apply the values with `helm upgrade ... -f values.yaml`. If you change
cert-manager's cluster resource namespace or service account, adjust
`secretAccess` and `certManager` in the chart values accordingly.

## Issue a staging certificate

Download the [versioned examples](https://github.com/dnscaleou/cert-manager-webhook-dnscale/tree/v1.0.0/examples)
into a local `examples/` directory, then replace their email and DNS names.
Start with Let's Encrypt staging, which issues certificates that browsers do
not trust:

```sh
kubectl apply -f examples/clusterissuer-staging.yaml
kubectl apply -f examples/certificate.yaml
kubectl wait certificate/dnscale-example --for=condition=Ready --timeout=300s
kubectl get orders,challenges
```

After validating issuance, renewal, and TXT cleanup, use a separate issuer with
`https://acme-v02.api.letsencrypt.org/directory` for production. The resulting
certificate is stored in the Kubernetes Secret named by `spec.secretName`.

Solver configuration:

```yaml
groupName: acme.dnscale.eu
solverName: dnscale
config:
  apiTokenSecretRef:
    name: dnscale-api-token
    key: api-token
  # Optional alternative API base URL; use HTTPS outside local tests.
  # apiUrl: https://api.dnscale.eu
```

The webhook honours cert-manager's resolved DNS name and zone, including CNAME
delegation when `cnameStrategy: Follow` is set in cert-manager's DNS-01 solver.
The token must cover the resulting validation zone.

## Verification

The original v1.0.0 live validation used Kubernetes 1.35.0 and cert-manager
1.19.3 and confirmed Let's Encrypt staging issuance, wildcard certificates,
renewal, and challenge TXT cleanup. The supported-version installation matrix
and upstream DNS01 conformance checks are described in
[the test guide](docs/testing.md).

```sh
make test
make test-conformance
helm lint deploy/dnscale-webhook --strict
```

CI runs cert-manager's upstream `RunConformance` suite in strict mode using
local API and DNS fixtures. It also builds the image, installs both supported
cert-manager versions and this chart in kind, waits for the webhook's APIService,
and checks that credential access is restricted.
The unit tests exercise the solver with Kubernetes Secret lookup and documented
API response fixtures, pagination, timeouts, repeated operations, and multiple
TXT values at one owner.

For a live API check, install the chart and Secret first, select the intended
Kubernetes context, and use a **disposable** challenge owner in an accessible
zone. The caller needs Kubernetes impersonation permission for the cert-manager
service account:

```sh
python3 scripts/verify-live.py \
  --zone your-test-domain.example \
  --token-file .test-api-token
```

This sends actual `Present` and `CleanUp` requests through the Kubernetes
APIService as cert-manager's service account. It checks repeated presentation,
two concurrent TXT values, selective and repeated cleanup, and restoration of
the original TXT values. It does not request an ACME certificate or verify
public DNS propagation. Use the staging Certificate flow above for that final
check. The script cleans up only its own randomly generated TXT values.

For repeatable live conformance, staging issuance, wildcard renewal, and
authoritative TXT cleanup checks, follow [the test guide](docs/testing.md).
The `Live DNS01 validation` workflow requires an explicit manual dispatch and
credentials configured in the protected `dns01-live` environment.

Some DNScale API versions return HTTP 500 when asked to remove content that is
already absent. On that response the webhook independently lists the records
and accepts cleanup only if the challenge value is confirmed absent. Other
errors, unreadable records, and values that still exist remain failures.

## Troubleshooting

- **Webhook unavailable:** inspect the APIService, deployment logs, and serving
  Certificate in the chart's namespace.
- **Secret forbidden/not found:** check `secretAccess`, Secret name/key, and the
  Issuer's resource namespace.
- **Zone not found:** check the token's zone access and the resolved validation
  zone. A zone created in DNScale still needs public delegation for ACME.
- **Challenge remains pending:** check authoritative TXT responses, public
  delegation, CAA policy, and cert-manager's Challenge status.

```sh
kubectl describe apiservice v1alpha1.acme.dnscale.eu
kubectl -n cert-manager logs deployment/cert-manager-webhook-dnscale
kubectl get challenges -A
```

## Releasing

See [the release procedure](docs/releasing.md) for version updates, tag pushes,
first-publication package visibility, and the anonymous installation gate.

## Operations and contribution

For operating this webhook alongside ExternalDNS, see
[Kubernetes DNS and TLS operations with DNScale](https://dnscale.eu/learning/kubernetes-dns-tls-operations).
See [maintainers and support](MAINTAINERS.md), [contribution instructions](CONTRIBUTING.md),
and [security reporting](SECURITY.md).

## License

[Apache-2.0](LICENSE).
