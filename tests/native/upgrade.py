#!/usr/bin/env python3
"""Prove that state written by a released provider upgrades without side effects.

Usage, inside scripts/disposable-pocketid.py:
    upgrade.py TOOL RELEASED_ARCHIVE NEW_BINARY

A released provider creates a confidential client with two federated
identities. An administrator then enables replay protection on one of them
outside Terraform. The new build takes over the same state and must not change
the client's identity or secret, and must leave each identity's replay
protection exactly as the server has it: through an unrelated update applied
WITHOUT a refresh, while state still predates the attribute, and afterwards
through an ordinary empty plan and another update. Everything lives in a
temporary directory; only the fixture is touched.
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
# Releases before 2.4.0 neither send nor record replay_protection.
tracks_replay = tuple(int(part) for part in released_version.split(".")) >= (2, 4, 0)
cid = "upgrade-" + uuid.uuid4().hex[:12]
OPEN_ISSUER = "https://open.example.invalid"  # stays unprotected
GUARDED_ISSUER = "https://guarded.example.invalid"  # an administrator protects this one


def api(method="GET", body=None):
    req = urllib.request.Request(base+"/api/oidc/clients/"+cid, method=method,
        headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"], "Content-Type": "application/json"},
        data=json.dumps(body).encode() if body is not None else None)
    with urllib.request.urlopen(req, timeout=15) as response:
        return json.load(response)


def server_replay_protection():
    identities = api()["credentials"]["federatedIdentities"]
    assert [i["issuer"] for i in identities] == [OPEN_ISSUER, GUARDED_ISSUER], "unexpected federated identities"
    return {i["issuer"]: i.get("replayProtection", False) for i in identities}


with tempfile.TemporaryDirectory(prefix="pocketid-upgrade-") as tmp:
    root = Path(tmp)
    mirror = root/"mirror"/SOURCE
    mirror.mkdir(parents=True)
    shutil.copy(released_archive, mirror/Path(released_archive).name)
    unpacked = mirror/DEV_VERSION/platform
    unpacked.mkdir(parents=True)
    shutil.copy(new_binary, unpacked/("terraform-provider-pocketid_v"+DEV_VERSION))
    (unpacked/("terraform-provider-pocketid_v"+DEV_VERSION)).chmod(0o755)

    rc = root/"provider.rc"
    rc.write_text('provider_installation {\n filesystem_mirror {\n path = '+json.dumps(str(root/"mirror"))+'\n include = ["'+SOURCE+'"]\n }\n}\n')
    work = root/"work"
    work.mkdir()
    env = dict(os.environ, TF_CLI_CONFIG_FILE=str(rc), TF_IN_AUTOMATION="1")
    for key in list(env):
        if key.startswith("TF_LOG") or key in ("TF_PLUGIN_CACHE_DIR", "TF_REATTACH_PROVIDERS"):
            del env[key]

    def config(provider_version, name):
        (work/"main.tf").write_text('''terraform {
 required_providers {
  pocketid = {
   source = "'''+SOURCE+'''"
   version = "'''+provider_version+'''"
  }
 }
}
provider "pocketid" {}
resource "pocketid_client" "test" {
 name = "'''+name+'''"
 client_id = "'''+cid+'''"
 callback_urls = ["https://example.invalid/callback"]
 is_public = false
 federated_identities = [
  { issuer = "'''+OPEN_ISSUER+'''", subject = "upgrade" },
  { issuer = "'''+GUARDED_ISSUER+'''", subject = "upgrade" },
 ]
}
''')

    def run(*args, ok=(0,)):
        # Output can carry state; keep it out of the test log.
        result = subprocess.run([tool, *args], cwd=work, env=env, capture_output=True)
        assert result.returncode in ok, tool+" "+args[0]+" exited "+str(result.returncode)
        return result

    def state():
        resources = json.loads(run("show", "-json").stdout)["values"]["root_module"]["resources"]
        return resources[0]["values"]

    # 1. The released provider creates the client. It never sends
    #    replayProtection, so the server stores both identities unprotected.
    config(released_version, "upgrade-fixture")
    run("init", "-input=false")
    run("apply", "-auto-approve", "-input=false")
    secret = state()["client_secret"]
    assert secret
    if tracks_replay:
        # 2.4.0 and later create new identities protected and record it, so a
        # same-schema patch upgrade only has to change nothing.
        expected = {OPEN_ISSUER: True, GUARDED_ISSUER: True}
        assert server_replay_protection() == expected
        config(DEV_VERSION, "upgrade-fixture")
        run("init", "-upgrade", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")  # exit 2 would mean a planned change
        config(DEV_VERSION, "upgrade-fixture-renamed")
        run("apply", "-auto-approve", "-input=false")
        assert state()["id"] == cid and state()["client_secret"] == secret, "upgrade changed identity or secret"
        assert server_replay_protection() == expected, "an unrelated update changed replay protection"
        run("plan", "-detailed-exitcode", "-input=false")
        run("destroy", "-auto-approve", "-input=false")
        print("PASS native "+tool+" upgrade "+released_version+" -> new build: empty plan, identity/secret and replay protection kept through an update")
        sys.exit(0)

    assert "replay_protection" not in state()["federated_identities"][0], "released provider state is not the expected shape"
    assert server_replay_protection() == {OPEN_ISSUER: False, GUARDED_ISSUER: False}

    # 2. An administrator protects one identity outside Terraform, as the admin UI would.
    client = api()
    client["credentials"]["federatedIdentities"][1]["replayProtection"] = True
    api("PUT", client)
    expected = {OPEN_ISSUER: False, GUARDED_ISSUER: True}
    assert server_replay_protection() == expected, "fixture could not enable replay protection"

    # 3. The new build applies an unrelated update WITHOUT refreshing, so state
    #    still has no replay_protection at all. Nothing may be assumed: the
    #    provider has to read the server's values before replacing the list.
    config(DEV_VERSION, "upgrade-fixture-renamed")
    run("init", "-upgrade", "-input=false")
    run("apply", "-refresh=false", "-auto-approve", "-input=false")
    assert server_replay_protection() == expected, "an unrefreshed update changed replay protection"
    after = state()
    assert after["id"] == cid and after["client_secret"] == secret, "upgrade changed identity or secret"
    assert [i["replay_protection"] for i in after["federated_identities"]] == [False, True], "state does not record the server's values"

    # 4. From here an ordinary plan is empty, and another unrelated update
    #    carries both settings through unchanged.
    run("plan", "-detailed-exitcode", "-input=false")  # exit 2 would mean a planned change
    config(DEV_VERSION, "upgrade-fixture-renamed-again")
    run("apply", "-auto-approve", "-input=false")
    assert state()["client_secret"] == secret, "update changed the secret"
    assert server_replay_protection() == expected, "an unrelated update changed replay protection"
    run("plan", "-detailed-exitcode", "-input=false")

    run("destroy", "-auto-approve", "-input=false")
    print("PASS native "+tool+" upgrade "+released_version+" -> new build: unrefreshed and refreshed updates keep identity, secret and each identity's replay protection; empty plans")
