# PocketID Provider Tests

The automated checks run from the repository root; [TESTING.md](../TESTING.md) says
what each covers and how to run it:

- `make test-acc`, `make test-acc-matrix`, `make test-acc-provider` and
  `make test-acc-supported` run the acceptance tests (build tag `acc`, in
  `internal/provider` and `internal/datasources`) against a disposable official
  Pocket ID image, 2.14.0 through 2.18.0.
- `scripts/disposable-pocketid.py VERSION -- COMMAND` is that fixture: it starts an
  isolated server on a free loopback port with a synthetic administrator, runs
  `COMMAND` against it and removes it. It is the only way tests get a server.
- `native/*.py` are native Terraform and OpenTofu scripts, run as children of the
  fixture: `lifecycle.py` (install from a mirror, create, import, delete),
  `upgrade.py`, `client_upgrade.py` and `upgrade_users_groups.py` (state written by
  a published release upgrades with the new build), and `application_config.py`
  (the application configuration, including the write-only password flow). Each
  runs with `terraform` and `tofu`; the upgrade proofs take the last release's
  verified published archive (or the binary unpacked from it). TESTING.md has the
  arguments, the commands and the Pocket ID versions each release runs them on.

The Terraform configurations in the two directories below are manual
demonstrations, not the automated suite. Use a disposable instance and the provider
installed from a filesystem mirror as described in [INSTALL.md](../INSTALL.md);
review all changes before applying.

## Test Structure

The tests are split into two separate configurations:

1. **terraform-resources/** - Creates all types of resources
2. **terraform-data-sources/** - Tests all data source lookups using the created resources

## Running the Tests

### Step 1: Create Resources

```bash
cd terraform-resources
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your PocketID instance details

terraform init
terraform apply

# Copy only the required non-sensitive resource IDs into the next configuration.
# Do not export all outputs: tokens and client secrets are present in state.
```

### Step 2: Test Data Sources

```bash
cd ../terraform-data-sources
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with:
# - Your PocketID instance details
# - Resource IDs from Step 1

terraform init
terraform apply

# Verify outputs
terraform output
```

### Step 3: Clean Up

```bash
# Clean up data source test first (no resources to destroy)
cd terraform-data-sources
terraform destroy

# Then clean up created resources
cd ../terraform-resources
terraform destroy
```

## What's Tested

### Resources

- Users (enabled/disabled states)
- Groups
- User-Group associations
- One-time access tokens (with different settings)
- OAuth2 clients (various types: web, mobile, SPA, service account)

### Data Sources

- Individual resource lookups by ID
- List all resources of each type
- Filtered queries (e.g., enabled users)
- Resource counts and aggregations

## Notes

- All test resources are prefixed with "test-" to avoid conflicts
- Sensitive outputs (tokens, secrets) are properly marked
- The tests are designed to be run in sequence (resources first, then data sources)
