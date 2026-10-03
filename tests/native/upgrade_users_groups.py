#!/usr/bin/env python3
"""Prove that user and group state written by a previous provider build upgrades
to this one with an empty plan.

Usage, inside scripts/disposable-pocketid.py:
    upgrade_users_groups.py TOOL OLD_BINARY NEW_BINARY

OLD_BINARY is a provider built from an earlier release's source (for example
`git archive v2.4.104`); NEW_BINARY is this build. Both are served from a
temporary filesystem mirror under two development version numbers. The old
build creates groups (with and without custom claims), a user with names,
groups and claims, a group membership for a user Terraform does not manage,
and a one-time access token, the way configurations written for it look. The
new build must then plan no change (with and without a refresh), apply an
unrelated update cleanly without changing any object's ID, and plan empty
again. A user without first_name and last_name that the old build imported
("" in state) shows the documented one in-place normalization update, with and
without a refresh, keeps its ID through it, and plans empty afterwards; one the
old build created (its create failed with "inconsistent result" and left it
tainted) is replaced once and then plans empty. Everything lives in a
temporary directory; only the fixture is touched.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import urllib.request
import uuid

tool, old_binary, new_binary = sys.argv[1:]
base = os.environ["POCKETID_BASE_URL"]
assert base.startswith("http://127.0.0.1:")
SOURCE = "registry.terraform.io/irashack/pocketid"
OLD_VERSION, NEW_VERSION = "98.0.0", "99.0.0"  # deliberately not release numbers
suffix = uuid.uuid4().hex[:8]


def api(method, path, body=None):
    req = urllib.request.Request(base + path, method=method,
        headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"], "Content-Type": "application/json"},
        data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req, timeout=15) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


# A user Terraform does not manage, for pocketid_group_membership.
outside = api("POST", "/api/users", {"username": "upgrade-outside-" + suffix, "email": "upgrade-outside-" + suffix + "@example.com"})

with tempfile.TemporaryDirectory(prefix="pocketid-upgrade-ug-") as tmp:
    root = Path(tmp)
    platform = subprocess.check_output(["go", "env", "GOOS"], text=True).strip() + "_" + \
        subprocess.check_output(["go", "env", "GOARCH"], text=True).strip()
    for version, binary in ((OLD_VERSION, old_binary), (NEW_VERSION, new_binary)):
        unpacked = root/"mirror"/SOURCE/version/platform
        unpacked.mkdir(parents=True)
        target = unpacked/("terraform-provider-pocketid_v" + version)
        shutil.copy(binary, target)
        target.chmod(0o755)
    rc = root/"provider.rc"
    rc.write_text('provider_installation {\n filesystem_mirror {\n path = ' + json.dumps(str(root/"mirror")) +
                  '\n include = ["' + SOURCE + '"]\n }\n}\n')
    work = root/"work"
    work.mkdir()
    env = dict(os.environ, TF_CLI_CONFIG_FILE=str(rc), TF_IN_AUTOMATION="1")
    for key in list(env):
        if key.startswith("TF_LOG") or key in ("TF_PLUGIN_CACHE_DIR", "TF_REATTACH_PROVIDERS"):
            del env[key]

    def config(version, friendly="Upgrade plain"):
        (work/"main.tf").write_text('''terraform {
 required_providers {
  pocketid = {
   source  = "''' + SOURCE + '''"
   version = "''' + version + '''"
  }
 }
}
provider "pocketid" {}
resource "pocketid_group" "plain" {
 name          = "upgrade-plain-''' + suffix + '''"
 friendly_name = "''' + friendly + '''"
}
resource "pocketid_group" "claims" {
 name          = "upgrade-claims-''' + suffix + '''"
 friendly_name = "Upgrade claims"
 custom_claims = { team = "platform" }
}
resource "pocketid_user" "full" {
 username       = "upgrade-full-''' + suffix + '''"
 email          = "upgrade-full-''' + suffix + '''@example.com"
 first_name     = "Up"
 last_name      = "Grade"
 locale         = "en"
 is_admin       = false
 email_verified = true
 groups         = [pocketid_group.plain.id, pocketid_group.claims.id]
 custom_claims  = { level = "senior" }
}
resource "pocketid_user" "minimal" {
 username   = "upgrade-minimal-''' + suffix + '''"
 email      = "upgrade-minimal-''' + suffix + '''@example.com"
 first_name = "Min"
 last_name  = "Imal"
}
resource "pocketid_group_membership" "outside" {
 group_id = pocketid_group.plain.id
 user_id  = "''' + outside["id"] + '''"
}
resource "pocketid_one_time_access_token" "minimal" {
 user_id = pocketid_user.minimal.id
 ttl     = "1h"
}
''')

    def run(*args, ok=(0,)):
        # Output can carry state; keep it out of the test log.
        result = subprocess.run([tool, *args], cwd=work, env=env, capture_output=True)
        assert result.returncode in ok, tool + " " + args[0] + " exited " + str(result.returncode)
        return result

    def values():
        resources = json.loads(run("show", "-json").stdout)["values"]["root_module"]["resources"]
        return {r["address"]: r["values"] for r in resources}

    def ids():
        return {address: v["id"] for address, v in values().items()}

    def planned(*args):
        """The planned resource changes (a saved plan, shown as JSON)."""
        run("plan", "-input=false", "-out=planned.tfplan", *args)
        return json.loads(run("show", "-json", "planned.tfplan").stdout).get("resource_changes", [])

    def noname(version):
        # A user without first_name and last_name: 2.4.104 recorded "" for
        # them (and its create failed with "inconsistent result").
        (work/"main.tf").write_text('terraform {\n required_providers {\n  pocketid = {\n   source  = "' + SOURCE +
            '"\n   version = "' + version + '"\n  }\n }\n}\nprovider "pocketid" {}\n'
            'resource "pocketid_user" "noname" {\n username = "upgrade-noname-' + suffix +
            '"\n email    = "upgrade-noname-' + suffix + '@example.com"\n}\n')

    try:
        config(OLD_VERSION)
        run("init", "-input=false")
        run("apply", "-auto-approve", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")  # the old build plans empty too
        before = ids()

        config(NEW_VERSION)
        run("init", "-upgrade", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")  # exit 2 would mean a planned change
        run("plan", "-detailed-exitcode", "-refresh=false", "-input=false")
        config(NEW_VERSION, "Upgrade plain renamed")
        run("apply", "-auto-approve", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")
        assert ids() == before, "an object's ID changed across the upgrade"
        # The attribute the new build added to pocketid_user and
        # pocketid_group_membership (an unresolved creation, see their
        # documentation) is null in state written by 2.4.104: the empty plans
        # above decoded that state, and the state this apply rewrote keeps it
        # null.
        for address in ("pocketid_user.full", "pocketid_user.minimal", "pocketid_group_membership.outside"):
            assert "unresolved_creation" in values()[address] and values()[address]["unresolved_creation"] is None, \
                address + ": unresolved_creation is not null after the upgrade"
        user = [r for r in json.loads(run("show", "-json").stdout)["values"]["root_module"]["resources"]
                if r["address"] == "pocketid_user.full"][0]["values"]
        held = api("GET", "/api/users/" + user["id"])
        assert sorted(g["id"] for g in held["userGroups"]) == sorted(user["groups"]), "groups changed"
        assert {c["key"]: c["value"] for c in held["customClaims"]} == {"level": "senior"}, "claims changed"
        run("destroy", "-auto-approve", "-input=false")

        # Omitted names, imported by 2.4.104: "" in state, a change on every
        # plan with the old build. The new build plans the documented one
        # in-place normalization update, with and without a refresh, keeps the
        # user's ID through it, and plans empty afterwards.
        noname_id = api("POST", "/api/users", {"username": "upgrade-noname-" + suffix, "email": "upgrade-noname-" + suffix + "@example.com"})["id"]
        noname(OLD_VERSION)
        run("init", "-upgrade", "-input=false")
        run("import", "-input=false", "pocketid_user.noname", noname_id)
        run("plan", "-detailed-exitcode", "-input=false", ok=(2,))  # 2.4.104 never converges here
        noname(NEW_VERSION)
        run("init", "-upgrade", "-input=false")
        run("plan", "-detailed-exitcode", "-refresh=false", "-input=false", ok=(2,))
        run("plan", "-detailed-exitcode", "-input=false", ok=(2,))
        # The one update must not also plan the new attribute as "known after
        # apply": it stays null.
        changes = planned("-refresh=false")
        assert [c["change"]["actions"] for c in changes] == [["update"]], "the normalization is not one in-place update"
        assert "unresolved_creation" not in (changes[0]["change"].get("after_unknown") or {}), "the update plans unresolved_creation as unknown"
        assert changes[0]["change"]["after"]["unresolved_creation"] is None
        run("apply", "-auto-approve", "-input=false")
        assert ids()["pocketid_user.noname"] == noname_id, "the normalization update replaced the user"
        run("plan", "-detailed-exitcode", "-input=false")
        run("plan", "-detailed-exitcode", "-refresh=false", "-input=false")
        run("destroy", "-auto-approve", "-input=false")

        # Omitted names, created by 2.4.104: its create fails with
        # "inconsistent result" and leaves the user tainted, so every apply
        # replaces it. The new build replaces it once more, then plans empty.
        noname(OLD_VERSION)
        run("init", "-upgrade", "-input=false")
        old_apply = run("apply", "-auto-approve", "-input=false", ok=(0, 1))
        assert old_apply.returncode == 1 and b"inconsistent result" in old_apply.stderr, "2.4.104 no longer fails this create"
        assert json.loads(run("state", "pull").stdout)["resources"][0]["instances"][0].get("status") == "tainted", "the failed create is not tainted"
        noname(NEW_VERSION)
        run("init", "-upgrade", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false", ok=(2,))
        run("apply", "-auto-approve", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")
        run("plan", "-detailed-exitcode", "-refresh=false", "-input=false")
        run("destroy", "-auto-approve", "-input=false")
    finally:
        api("DELETE", "/api/users/" + outside["id"])
    print("PASS native " + tool + " users/groups upgrade: empty plan (refreshed and not) after switching builds, IDs kept through an unrelated update, "
          "empty plan again; omitted names: an imported user gets one in-place normalization update (refreshed and not) keeping its ID, "
          "a user whose 2.4.104 create failed (tainted) is replaced once; then empty plans")
