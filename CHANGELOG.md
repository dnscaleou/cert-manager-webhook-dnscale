# Changelog

## v1.0.1

- Align the webhook with cert-manager 1.21.2 and Kubernetes libraries 0.36.2;
  source builds now require Go 1.26.
- Add the Apache-2.0 license to the repository and container image, plus
  maintainer, contribution, security, and testing documentation.
- Run cert-manager's official DNS01 conformance suite in strict mode with local
  API/DNS fixtures, including preservation of sibling TXT values.
- Test installation, APIService readiness, and scoped Secret access with
  cert-manager 1.20.4 and 1.21.2 on Kubernetes 1.35.0.
- Provide an opt-in live conformance workflow and a Let's Encrypt staging
  issuance/renewal script. Live validation requires a disposable delegated zone
  and explicitly supplied scoped credentials; normal CI does not run it.

The image is tagged `v1.0.1` and the Helm chart is version `1.0.1`. Existing
Issuer configuration and chart resource names are unchanged. The published
v1.0.0 image, chart, and tag remain immutable.

## v1.0.0

Initial public image and OCI chart release, including installation examples,
anonymous artifact verification, scoped Secret RBAC, and selective TXT cleanup.
