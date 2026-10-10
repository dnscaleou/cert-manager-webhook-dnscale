# Webhook verification

Use Go 1.26, Python 3, Helm, Docker, kind, and kubectl. The Go dependencies track
cert-manager 1.21.2 and Kubernetes libraries 0.36.2. The cluster installation
matrix uses Kubernetes 1.35.0 with cert-manager 1.20.4 and 1.21.2.

## Unit tests and conformance

```sh
make test
make test-conformance
helm lint deploy/dnscale-webhook --strict
```

The Makefile uses a repository-local Go cache. `scripts/setup-envtest.py` downloads
the pinned Kubernetes 1.35.0 envtest release from the upstream controller-tools
repository and verifies its SHA-512 checksum before installing the binaries.
Set `KUBEBUILDER_ASSETS` to use an existing envtest installation instead. Its
Kubernetes control plane is isolated from the current kubeconfig.

`TestDNS01Conformance` calls the upstream cert-manager `RunConformance` suite
with strict mode enabled. It uses the actual solver, real temporary Kubernetes
API servers and Secret lookup, a DNScale API fixture, and a local DNS server.
Both basic propagation/cleanup and preservation of another TXT value are tested.
Ordinary PR CI runs these checks without DNScale credentials or public DNS writes.
The conformance build tag keeps envtest out of ordinary unit-test runs.

## Live conformance

Use a dedicated, publicly delegated test zone. The token needs `zones:read`,
`records:read`, and `records:write` restricted to that zone. Tests create random
names within it; exact DNS-name token scopes must allow those generated owners.
Keep the token outside the repository, or in the ignored `.test-api-token` file.

```sh
export DNSCALE_LIVE_TESTS=true
export DNSCALE_TEST_ZONE=your-disposable-zone.example
export DNSCALE_TEST_TOKEN_FILE=/secure/path/dnscale-token
make test-live-conformance
```

`TestDNS01LiveConformance` runs the same strict upstream suite against DNScale
and public authoritative DNS. It generates a unique challenge owner for every
run, uses a temporary Kubernetes API server and Secret, and cleans up the suite's
TXT values. This test never uses your cluster kubeconfig. It permits up to ten
minutes for each DNS propagation check. An optional `DNSCALE_TEST_API_URL`
selects an alternative HTTPS API.

Secret fixtures are created in a temporary directory with mode 0600. Kubernetes
debug logging is disabled before creating Secrets. Test output can contain the
test hostname and public challenge values; publish only sanitized summaries.
If a run is forcibly interrupted, inspect its printed challenge owner and remove
only its test values after confirming no certificate issuance is still active.

## Staging issuance and renewal

Install the webhook in a disposable cluster first. Install
[cmctl](https://github.com/cert-manager/cmctl/releases) and `dig`, then run:

```sh
python3 scripts/verify-staging.py \
  --context your-disposable-context \
  --zone your-disposable-zone.example \
  --email operator@example.com \
  --token-file /secure/path/dnscale-token
```

The script creates a unique namespace, scoped credential Secret and RoleBinding,
ClusterIssuer, and Certificate for a random hostname plus its wildcard. It uses
only Let's Encrypt staging. It checks initial issuance, TXT cleanup at every
authoritative nameserver, `cmctl renew`, a higher Certificate revision, a changed
certificate, and final TXT cleanup. It deletes its namespace, issuer, and Secrets.
It never prints tokens or certificate private keys.

Pass `--webhook-service-account` when using a different chart fullname, and
`--cert-manager-namespace` when changing the cert-manager namespace. The default
webhook service account is `cert-manager-webhook-dnscale`. If DNS cleanup or
namespace deletion fails, the script keeps the temporary issuer and credentials
available for repair. Inspect the named namespace, resume cleanup, then remove
the matching temporary ClusterIssuer, Role, RoleBinding, and Secrets.

## Protected live workflow

The `Live DNS01 validation` workflow runs only through a manual dispatch on
`master`. Configure the `dns01-live` GitHub environment with:

- Required reviewers and deployment branches restricted to `master`.
- Secret `DNSCALE_TEST_API_TOKEN` containing the scoped disposable-zone token.
- Variables `DNSCALE_TEST_ZONE` and `ACME_EMAIL`.

The workflow runs conformance and staging issuance/renewal sequentially for both
supported cert-manager versions. Credentials are never exposed to pull-request
jobs, and the temporary token file is removed even after failure. Rotate or
revoke the token when retiring the test environment.
