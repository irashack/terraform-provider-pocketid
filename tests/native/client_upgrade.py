#!/usr/bin/env python3
"""Prove that pocketid_client state written by a released 2.4.x provider keeps
working after the 3.0 client changes.

Usage, inside scripts/disposable-pocketid.py:
    client_upgrade.py TOOL RELEASED_ARCHIVE NEW_BINARY

The released provider creates clients shaped like the homelab's: fixed
client_id, launch_url, prevent_destroy, and allowed_user_groups written as a
sorted list; plus a public restricted client and one with a generated ID.
The new build then takes over the same state, with the same configuration:

- the refreshed plan is empty (list-to-set, client_id, the new computed
  attributes, generate_secret and client_secret_id are filled without a
  planned change), and refresh records client_secret_id for the secret the
  released provider stored;
- generate_secret = false applied WITHOUT a refresh, while state still
  predates client_secret_id, revokes exactly the stored secret, identified
  by its prefix, and keeps the group restriction;
- generate_secret = true generates a new one in place;
- removing a client's groups leaves it restricted (to nobody), stably;
- a client restricted outside Terraform after the last refresh is not opened
  by an unrefreshed update; the apply asks for a refresh;
- a different client_id plans a replacement, which prevent_destroy refuses.

Everything lives in a temporary directory; only the fixture is touched.
Output that can carry state is never printed.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.request
import uuid

tool, released_archive, new_binary = sys.argv[1:]
base = os.environ["POCKETID_BASE_URL"]
assert base.startswith("http://127.0.0.1:")
SOURCE = "registry.terraform.io/irashack/pocketid"
DEV_VERSION = "99.0.0"  # deliberately not a release number
released = re.fullmatch(r"terraform-provider-pocketid_(\d+\.\d+\.\d+)_(\w+_\w+)\.zip", Path(released_archive).name)
assert released, "released archive must keep its published file name"
released_version, platform = released.groups()
suffix = uuid.uuid4().hex[:10]
HOMELAB, PUBLIC = "upg-home-" + suffix, "upg-pub-" + suffix


def api(path):
    req = urllib.request.Request(base + path, headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"]})
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)


def api_put(path, body):
    req = urllib.request.Request(base + path, method="PUT", data=json.dumps(body).encode(),
        headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"], "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)


def secrets_of(cid):
    return api("/api/oidc/clients/" + cid + "/secrets") or []


with tempfile.TemporaryDirectory(prefix="pocketid-client-upgrade-") as tmp:
    root = Path(tmp)
    mirror = root / "mirror" / SOURCE
    mirror.mkdir(parents=True)
    shutil.copy(released_archive, mirror / Path(released_archive).name)
    unpacked = mirror / DEV_VERSION / platform
    unpacked.mkdir(parents=True)
    shutil.copy(new_binary, unpacked / ("terraform-provider-pocketid_v" + DEV_VERSION))
    (unpacked / ("terraform-provider-pocketid_v" + DEV_VERSION)).chmod(0o755)
    rc = root / "provider.rc"
    rc.write_text('provider_installation {\n filesystem_mirror {\n path = ' + json.dumps(str(root / "mirror")) + '\n include = ["' + SOURCE + '"]\n }\n}\n')
    work = root / "work"
    work.mkdir()
    env = dict(os.environ, TF_CLI_CONFIG_FILE=str(rc), TF_IN_AUTOMATION="1")
    for key in list(env):
        if key.startswith("TF_LOG") or key in ("TF_PLUGIN_CACHE_DIR", "TF_REATTACH_PROVIDERS"):
            del env[key]

    def config(version, home_extra="", public_groups="[pocketid_group.a.id]", home_id=HOMELAB, protect=True, generated_name="Generated ID"):
        lifecycle = "\n lifecycle {\n  prevent_destroy = true\n }\n" if protect else ""
        groups = "" if public_groups is None else " allowed_user_groups = " + public_groups + "\n"
        (work / "main.tf").write_text('''terraform {
 required_providers {
  pocketid = {
   source = "''' + SOURCE + '''"
   version = "''' + version + '''"
  }
 }
}
provider "pocketid" {}
resource "pocketid_group" "a" {
 name = "upg-a-''' + suffix + '''"
 friendly_name = "upg-a-''' + suffix + '''"
}
resource "pocketid_group" "b" {
 name = "upg-b-''' + suffix + '''"
 friendly_name = "upg-b-''' + suffix + '''"
}
resource "pocketid_client" "home" {
 name = "Homelab-shaped"
 client_id = "''' + home_id + '''"
 callback_urls = ["https://home.example.invalid/callback"]
 launch_url = "https://home.example.invalid"
 is_public = false
 pkce_enabled = true
 allowed_user_groups = sort([pocketid_group.b.id, pocketid_group.a.id])
''' + home_extra + lifecycle + '''}
resource "pocketid_client" "public" {
 name = "Public restricted"
 client_id = "''' + PUBLIC + '''"
 callback_urls = ["https://public.example.invalid/callback"]
 is_public = true
 pkce_enabled = true
''' + groups + lifecycle + '''}
resource "pocketid_client" "generated" {
 name = "''' + generated_name + '''"
 callback_urls = ["https://generated.example.invalid/callback"]
 logout_callback_urls = ["https://generated.example.invalid/logout"]
''' + lifecycle + '''}
''')

    def run(*args, ok=(0,)):
        # Output can carry state; keep it out of the test log.
        result = subprocess.run([tool, *args], cwd=work, env=env, capture_output=True)
        assert result.returncode in ok, tool + " " + args[0] + " exited " + str(result.returncode)
        return result

    def state():
        resources = json.loads(run("show", "-json").stdout)["values"]["root_module"]["resources"]
        return {r["name"]: r["values"] for r in resources if r["type"] == "pocketid_client"}

    def restriction(cid):
        client = api("/api/oidc/clients/" + cid)
        return client["isGroupRestricted"], sorted(g["id"] for g in client.get("allowedUserGroups") or [])

    # 1. The released provider creates the clients.
    config(released_version)
    run("init", "-input=false")
    run("apply", "-auto-approve", "-input=false")
    old = state()
    secret = old["home"]["client_secret"]
    assert secret and "client_secret_id" not in old["home"] and "is_group_restricted" not in old["home"], "released state is not the expected shape"
    assert isinstance(old["home"]["allowed_user_groups"], list) and len(old["home"]["allowed_user_groups"]) == 2
    home_restriction = restriction(HOMELAB)
    assert home_restriction[0] and len(home_restriction[1]) == 2
    released_secrets = secrets_of(HOMELAB)
    matching = [s for s in released_secrets if s.get("prefix") and secret.startswith(s["prefix"])]
    assert len(matching) == 1, "the stored secret is not identifiable on the server"
    stored_id = matching[0]["id"]

    # 2. The new build: the refreshed plan with the same configuration is
    #    empty (exit 2 would mean a planned change).
    config(DEV_VERSION)
    run("init", "-upgrade", "-input=false")
    run("plan", "-detailed-exitcode", "-input=false")

    # 3. Without a refresh, while state still predates client_secret_id and
    #    is_group_restricted, generate_secret = false revokes exactly the
    #    stored secret (found by its prefix) and keeps the restriction.
    config(DEV_VERSION, home_extra=" generate_secret = false\n")
    run("apply", "-refresh=false", "-auto-approve", "-input=false")
    after = secrets_of(HOMELAB)
    assert stored_id not in [s["id"] for s in after], "the stored secret was not revoked"
    assert len(after) == len(released_secrets) - 1, "a secret other than the stored one was revoked"
    assert restriction(HOMELAB) == home_restriction, "the group restriction changed"
    current = state()
    assert current["home"]["id"] == HOMELAB and current["home"]["client_secret"] is None and current["home"]["client_secret_id"] is None
    assert api("/api/oidc/clients/" + HOMELAB).get("launchURL") == "https://home.example.invalid"
    run("plan", "-detailed-exitcode", "-input=false")

    # 4. generate_secret = true generates a new secret in place; refresh
    #    keeps its ID; the plan is empty.
    config(DEV_VERSION)
    run("apply", "-auto-approve", "-input=false")
    current = state()
    new_id = current["home"]["client_secret_id"]
    assert current["home"]["id"] == HOMELAB and current["home"]["client_secret"] and new_id
    assert new_id in [s["id"] for s in secrets_of(HOMELAB)]
    run("plan", "-detailed-exitcode", "-input=false")
    assert current["generated"]["client_id"] == current["generated"]["id"]
    assert current["public"]["is_group_restricted"] is True

    # 5. Removing the public client's groups leaves it restricted, stably.
    config(DEV_VERSION, public_groups=None)
    run("apply", "-auto-approve", "-input=false")
    assert restriction(PUBLIC) == (True, []), "removing the groups opened the client"
    run("plan", "-detailed-exitcode", "-input=false")

    # 6. A client restricted outside Terraform since the last refresh is not
    #    opened by an unrefreshed update that leaves is_group_restricted
    #    unset: the apply refuses and asks for a refresh; refreshed, the
    #    update keeps the restriction.
    generated_id = current["generated"]["id"]
    client = api("/api/oidc/clients/" + generated_id)
    client["isGroupRestricted"] = True
    api_put("/api/oidc/clients/" + generated_id, client)
    assert restriction(generated_id) == (True, [])
    config(DEV_VERSION, public_groups=None, generated_name="Generated ID renamed")
    refused = run("apply", "-refresh=false", "-auto-approve", "-input=false", ok=(1,))
    assert b"refresh" in refused.stderr, "the unrefreshed apply failed for another reason"
    assert restriction(generated_id) == (True, []), "an unrefreshed update opened the client"
    assert api("/api/oidc/clients/" + generated_id)["name"] == "Generated ID", "the refused update changed the client"
    run("apply", "-auto-approve", "-input=false")
    assert restriction(generated_id) == (True, []), "a refreshed update opened the client"
    assert api("/api/oidc/clients/" + generated_id)["name"] == "Generated ID renamed"
    run("plan", "-detailed-exitcode", "-input=false")

    # 7. A different client_id plans a replacement; prevent_destroy refuses it.
    config(DEV_VERSION, public_groups=None, home_id=HOMELAB + "-renamed", generated_name="Generated ID renamed")
    refused = run("plan", "-input=false", ok=(1,))
    assert b"prevent_destroy" in refused.stderr, "the plan failed for another reason than prevent_destroy"

    config(DEV_VERSION, public_groups=None, protect=False, generated_name="Generated ID renamed")
    run("destroy", "-auto-approve", "-input=false")
    print("PASS native " + tool + " client upgrade " + released_version + " -> new build: empty refreshed plan; unrefreshed generate_secret=false revoked only the stored secret by prefix; regenerated in place; groups removal kept the restriction; an unrefreshed update refused to open a client restricted outside Terraform; client_id change refused by prevent_destroy; secrets after the released create: " + str(len(released_secrets)))
