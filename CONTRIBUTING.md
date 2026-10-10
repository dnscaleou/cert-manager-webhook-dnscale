# Contributing

Small, focused pull requests are welcome. Explain the problem, expected behavior,
and verification performed. Contributions are provided under the
[Apache-2.0 license](LICENSE). See [MAINTAINERS.md](MAINTAINERS.md) for support.

Use Go 1.26 and follow the [test guide](docs/testing.md). Run Go
formatting, race tests, vet, and strict Helm lint before submitting. Changes to
record handling must preserve other TXT values and remain safe when retried.
Changes to the chart must retain named-Secret access restrictions.

Keep API tokens, Secret manifests with credentials, kubeconfigs, customer names,
and private test reports out of Git. Live DNS tests use disposable, delegated
zones and explicit credentials; they must never run automatically on untrusted
pull requests. See [SECURITY.md](SECURITY.md) for private vulnerability reports.

Use concise commit subjects such as `fix: preserve sibling TXT values` or
`test: cover repeated cleanup`. Describe changes to tested versions in the
README. Follow [the release procedure](docs/releasing.md) for version updates.

