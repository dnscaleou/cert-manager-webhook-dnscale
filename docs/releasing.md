# Release procedure

Releases use Git tags such as `v1.0.0`. The image uses that exact tag, while the
Helm chart uses the corresponding SemVer version without `v` (`1.0.0`).

1. Update `version` and `appVersion` in `deploy/dnscale-webhook/Chart.yaml`,
   `image.tag` in `values.yaml`, the README installation examples, and `CHANGELOG.md`. The chart
   name must remain `cert-manager-webhook-dnscale` to match its OCI path. Keep
   the default `nameOverride` to preserve names and selectors for source installs.
2. Run `make test`, `make test-conformance`, the Python script tests
   (`python3 -m unittest discover -s scripts -p 'test_*.py'`), strict Helm lint,
   and workflow validation. Review the installation matrix in CI and the
   [test guide](testing.md). Record live test results separately from fixture
   conformance; ordinary CI does not perform public DNS writes.
   Commit the release checkpoint and push `master`.
3. Create and push an annotated tag, for example:

   ```sh
   git tag -a v1.0.0 -m 'Release v1.0.0'
   git push origin v1.0.0
   ```

The Release workflow reruns CI, including installation in kind. Only after that
passes does it publish the Linux AMD64/ARM64 image and OCI chart, attach the
chart archive and `SHA256SUMS` to a draft GitHub release, and run verification on
a fresh runner without registry login. That final job checks anonymous access,
image platforms and digest, matching chart archives, an actual installation of
the published artifacts, APIService availability, and credential Secret RBAC.
It publishes the GitHub release only when these checks pass.

## First publication

New GHCR packages can be private even when their source repository is public.
In the DNScale organization's GitHub package settings, check that both
`cert-manager-webhook-dnscale` and `charts/cert-manager-webhook-dnscale` are public;
change their visibility if needed.
Confirm that both are associated with this repository and inherit its access.
If the anonymous verification job ran before visibility was changed, rerun only
the failed jobs after changing visibility. Leave the release in draft until
verification succeeds; do not bypass the installation gate.

The image source label and chart source annotation identify this repository.
The workflow needs `packages: write` to publish and `contents: write` to manage
the draft and final GitHub release. It uses `GITHUB_TOKEN`, not a stored personal
access token.

## Verify or recover

```sh
python3 scripts/verify-release.py v1.0.0
helm pull oci://ghcr.io/dnscaleou/charts/cert-manager-webhook-dnscale --version 1.0.0
```

A retry may refresh an unpublished draft. Published versions must never be
retagged or overwritten: fix the source, increment the versions, and create a
new release. The publish job refuses to overwrite a published GitHub release.
If a source fix is needed before the first tag is published successfully, keep
the failed tag for diagnosis and use a new version rather than moving it.
