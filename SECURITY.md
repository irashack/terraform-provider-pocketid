# Security

Report vulnerabilities privately through
[GitHub security advisories](https://github.com/irashack/terraform-provider-pocketid/security/advisories/new).
Do not include credentials, state, saved plans, production logs or private deployments.
Use a minimal reproduction with synthetic values. This volunteer maintenance fork
has no guaranteed response or remediation timeline; fixes target the latest fork release.

Use [Issues](https://github.com/irashack/terraform-provider-pocketid/issues) for ordinary bugs.
Upstream's contact address and support commitments do not apply to this fork.

## Protect your deployment

- Supply API tokens through the environment or your secret manager.
- Use HTTPS with certificate verification for remote Pocket ID instances. HTTP is
  supported for local tests; the provider does not enforce HTTPS for you.
- Protect state, backups and saved plans with access controls and encryption.
  `sensitive` hides normal display; it does not encrypt stored secrets.
- Import and refresh do not retrieve or rotate an existing client secret.
- After ambiguous creation failure, inspect the client before applying again;
  Terraform may mark the resource tainted and propose replacement.

HTTP diagnostics omit arbitrary response bodies. Terraform and third-party test
harnesses may still print state or private inputs, so never publish complete debug
logs or crash dumps. See [README.md](README.md#secret-and-failure-behavior) for the
client failure contract and [TESTING.md](TESTING.md) for disposable test guidance.
