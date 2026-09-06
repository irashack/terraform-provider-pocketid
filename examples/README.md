# Examples

Install the fork with the filesystem mirror in [INSTALL.md](../INSTALL.md) first.
Every runnable example pins `registry.terraform.io/irashack/pocketid` at `2.3.1`;
registry publication is pending. Set `TF_CLI_CONFIG_FILE` to your mirror configuration.

- [Basic client](basic-client/): one confidential OIDC application.
- [SPA with PKCE](spa-with-pkce/): a public browser application.
- [User management](user-management/): users and groups.
- [Complete](complete/): a larger demonstration; review every resource before applying.
- `resources/` and `data-sources/`: snippets used in generated documentation.

Read each example's variables before applying. Use a disposable Pocket ID instance;
the complete examples can alter instance settings and create users. Keep credentials,
state, saved plans and local `.tfvars` out of git. Sensitive outputs are still in state.
For automated checks that set up and remove their own server, use `make test-acc`.
