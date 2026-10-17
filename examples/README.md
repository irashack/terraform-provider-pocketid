# Examples

`terraform init` or `tofu init` installs the provider from the registry; every
runnable example requires `irashack/pocketid` `~> 3.1`. To install a verified
archive through a filesystem mirror instead, follow [INSTALL.md](../INSTALL.md),
use its explicit `registry.terraform.io/irashack/pocketid` source address and set
`TF_CLI_CONFIG_FILE` to your mirror configuration. The provider is maintained
independently of its upstream origin; see [UPSTREAM.md](../UPSTREAM.md).

- [Basic client](basic-client/): one confidential OIDC application.
- [SPA with PKCE](spa-with-pkce/): a public browser application.
- [User management](user-management/): users and groups.
- [Complete](complete/): a larger demonstration; review every resource before applying.
- `resources/` and `data-sources/`: snippets used in generated documentation.

Read each example's variables before applying. Use a disposable Pocket ID instance;
the complete examples can alter instance settings and create users. Keep credentials,
state, saved plans and local `.tfvars` out of git. Sensitive outputs are still in state.
For automated checks that set up and remove their own server, use `make test-acc`.
