---
page_title: "pocketid_group_members Resource - terraform-provider-pocketid"
subcategory: ""
description: |-
  Owns the whole membership of one Pocket-ID group: the users in user_ids are exactly the group's members. Use it when Terraform should be the only writer of who is in the group; use pocketid_group_membership to add single users to a group that others also change.
  The group is written with one request (PUT /api/user-groups/{id}/users) that replaces the membership, so a plan that shows the new set is the whole change. A refresh reads the group's actual members, so a user added or removed outside Terraform shows up as a difference.
  ~> Do not combine with other writers of the same group's membership For one group, do not use this resource together with pocketid_group_membership, with the groups attribute of pocketid_user on any user who is, or should be, a member, or with a second pocketid_group_members. Each of those replaces or edits the same membership list, and they undo each other's changes on every apply.
  ~> Adopting a group that already has members Creating this resource refuses to remove members that user_ids does not list, because a plan for a new resource cannot show them. Import the group first (terraform import pocketid_group_members.<name> <group_id>), so the plan shows each member that would be removed, or list every current member.
  ~> Removing members can end sessions (Pocket ID 2.17) When a user stops being a member, Pocket ID 2.17 can sign that user out of group-restricted OIDC clients that have a back-channel logout URL, if the group was what let them in. Removing a user from user_ids and destroying this resource both remove members.
  ~> Concurrent changes by others Pocket ID can only replace a group's whole member list. Before every write the provider reads the group's members: create and update refuse to remove a member that the plan did not show, and destroy keeps every member that is not in user_ids. Those checks cover what the group held at that read only. A user added by something else (the Pocket ID admin interface, another Terraform run, an onboarding service) after the read and before the write, an instant later, cannot be protected: the write removes them, no check can prevent it, and the provider cannot tell afterwards that it happened. Within one Terraform run the provider serializes every write of user-group relations, so this resource, pocketid_group_membership and pocketid_user do not overwrite each other.
  ~> LDAP groups A group synchronized from LDAP gets its membership rewritten by the next LDAP synchronization. Do not manage its members with this resource.
  Partial results. If Pocket ID applies only part of a request (it skips an ID that names no user), the resource reports the error and still records the members the group actually holds, so that Terraform marks it tainted and destroying it removes those members; nothing is left unmanaged. When a request fails in a way that does not show whether it was applied, the group is read once and what it holds is recorded; if that read fails too, the members last read are kept and a refresh reconciles them.
  Destroying the resource removes the users in user_ids from the group. Members added outside Terraform since the last refresh stay. The group itself is not deleted.
---

# pocketid_group_members (Resource)

Owns the whole membership of one Pocket-ID group: the users in `user_ids` are exactly the group's members. Use it when Terraform should be the only writer of who is in the group; use `pocketid_group_membership` to add single users to a group that others also change.

The group is written with one request (`PUT /api/user-groups/{id}/users`) that replaces the membership, so a plan that shows the new set is the whole change. A refresh reads the group's actual members, so a user added or removed outside Terraform shows up as a difference.

~> **Do not combine with other writers of the same group's membership** For one group, do not use this resource together with `pocketid_group_membership`, with the `groups` attribute of `pocketid_user` on any user who is, or should be, a member, or with a second `pocketid_group_members`. Each of those replaces or edits the same membership list, and they undo each other's changes on every apply.

~> **Adopting a group that already has members** Creating this resource refuses to remove members that `user_ids` does not list, because a plan for a new resource cannot show them. Import the group first (`terraform import pocketid_group_members.<name> <group_id>`), so the plan shows each member that would be removed, or list every current member.

~> **Removing members can end sessions (Pocket ID 2.17)** When a user stops being a member, Pocket ID 2.17 can sign that user out of group-restricted OIDC clients that have a back-channel logout URL, if the group was what let them in. Removing a user from `user_ids` and destroying this resource both remove members.

~> **Concurrent changes by others** Pocket ID can only replace a group's whole member list. Before every write the provider reads the group's members: create and update refuse to remove a member that the plan did not show, and destroy keeps every member that is not in `user_ids`. Those checks cover what the group held at that read only. A user added by something else (the Pocket ID admin interface, another Terraform run, an onboarding service) after the read and before the write, an instant later, cannot be protected: the write removes them, no check can prevent it, and the provider cannot tell afterwards that it happened. Within one Terraform run the provider serializes every write of user-group relations, so this resource, `pocketid_group_membership` and `pocketid_user` do not overwrite each other.

~> **LDAP groups** A group synchronized from LDAP gets its membership rewritten by the next LDAP synchronization. Do not manage its members with this resource.

**Partial results.** If Pocket ID applies only part of a request (it skips an ID that names no user), the resource reports the error and still records the members the group actually holds, so that Terraform marks it tainted and destroying it removes those members; nothing is left unmanaged. When a request fails in a way that does not show whether it was applied, the group is read once and what it holds is recorded; if that read fails too, the members last read are kept and a refresh reconciles them.

**Destroying** the resource removes the users in `user_ids` from the group. Members added outside Terraform since the last refresh stay. The group itself is not deleted.

## Example Usage

```terraform
resource "pocketid_group" "admins" {
  name          = "admins"
  friendly_name = "Administrators"
}

data "pocketid_user" "alice" {
  username = "alice"
}

data "pocketid_user" "bob" {
  username = "bob"
}

# The group's members are exactly these two users. Anyone else who is added to
# the group outside Terraform shows up as a difference in the next plan and is
# removed by the next apply.
resource "pocketid_group_members" "admins" {
  group_id = pocketid_group.admins.id
  user_ids = [data.pocketid_user.alice.id, data.pocketid_user.bob.id]
}

# A group that must have no members.
resource "pocketid_group" "quarantine" {
  name          = "quarantine"
  friendly_name = "Quarantine"
}

resource "pocketid_group_members" "quarantine" {
  group_id = pocketid_group.quarantine.id
  user_ids = []
}
```

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `group_id` (String) The ID of the group (a UUID). Changing this forces a new resource.
- `user_ids` (Set of String) The IDs of the users (UUIDs) that are the group's members, no more and no fewer. An empty set means the group has no members. An ID that names no user is an error, not skipped.

### Read-Only

- `id` (String) The resource ID, the same as `group_id`.

## Import

Import is supported using the following syntax:

The [`terraform import` command](https://developer.hashicorp.com/terraform/cli/commands/import) can be used, for example:

```shell
# A group's membership is imported using the group's ID
terraform import pocketid_group_members.admins "3fa2c1d4-0000-4000-8000-000000000001"
```

```terraform
# Adopt a group that already has members: import it first, so that the plan
# shows which members the configuration would remove.
# terraform import pocketid_group_members.admins "3fa2c1d4-0000-4000-8000-000000000001"

import {
  to = pocketid_group_members.admins
  id = "3fa2c1d4-0000-4000-8000-000000000001"
}

resource "pocketid_group_members" "admins" {
  group_id = "3fa2c1d4-0000-4000-8000-000000000001"
  user_ids = ["7b9e0a12-0000-4000-8000-000000000001"]
}
```
