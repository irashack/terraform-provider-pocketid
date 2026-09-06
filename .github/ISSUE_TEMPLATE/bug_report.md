---
name: Bug Report
about: Create a report to help us improve
title: '[BUG] '
labels: 'bug, needs-triage'
assignees: ''

---

## Bug Description

A clear and concise description of what the bug is.

## Terraform / OpenTofu and Pocket ID Versions

Include the CLI version, Pocket ID version, operating system and architecture.

## Provider Version

Include the exact provider source address and version; distinguish the fork from upstream.

## Affected Resource(s)

Please list the resources as a list, for example:

- pocketid_user
- pocketid_group

If this issue appears to affect multiple resources, it may be an issue with Terraform's core, so please mention this.

## Terraform Configuration Files

```hcl
# Copy-paste your Terraform configuration here.
# Please remove any sensitive information like API keys.
```

## Debug Output

Include only a short, redacted error and a synthetic reproduction. Never attach full debug logs, state, plans, API tokens or client secrets.

## Expected Behavior

What should have happened?

## Actual Behavior

What actually happened?

## Steps to Reproduce

Please list the steps required to reproduce the issue, for example:

1. `terraform apply`

## Important Factoids

Are there anything atypical about your accounts that we should know? For example: Running in a VPN environment, using a proxy, etc.

## References

Are there any other GitHub issues (open or closed) or Pull Requests that should be linked here? For example:

- #0000
