---
name: Provider Crash Report
about: Report a provider crash or panic
title: '[CRASH] '
labels: 'bug, crash, priority-high'
assignees: ''

---

## Crash Summary

Brief description of what you were doing when the provider crashed.

## Terraform / OpenTofu and Pocket ID Versions

Include the CLI version, Pocket ID version, operating system and architecture.

## Provider Version

Version of terraform-provider-pocketid that crashed.

## Terraform Configuration

```hcl
# Minimal configuration that reproduces the crash
# Please remove any sensitive information
```

## Crash Output

```
# Include only a redacted stack trace with no state, secrets or private inputs
# This typically starts with "panic:" and includes a stack trace
```

## Steps to Reproduce

1.
2.
3.

## Debug Logs

Include only a short, redacted error and a synthetic reproduction. Never attach full debug logs, state, plans, API tokens or client secrets.

## Environment Details

- Operating System:
- Architecture (x86_64, arm64, etc.):
- Any special network configuration (proxy, VPN, etc.):

## Workaround

Have you found any way to avoid the crash? If so, please describe.

## Note

Reports are reviewed as maintainer time allows.
