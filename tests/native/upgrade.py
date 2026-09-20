#!/usr/bin/env python3
"""Prove that state written by a released provider upgrades without side effects.

Usage, inside scripts/disposable-pocketid.py:
    upgrade.py TOOL RELEASED_ARCHIVE NEW_BINARY

A released provider creates a confidential client with a federated identity.
The new build then takes over the same state and must plan nothing, must not
change the client's identity or secret, and must leave the identity's
replay protection exactly as the server had it, including through an unrelated
update. Everything lives in a temporary directory; only the fixture is touched.
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
cid = "upgrade-" + uuid.uuid4().hex[:12]
ISSUER = "https://issuer.example.invalid"


def server_identity():
    req = urllib.request.Request(base+"/api/oidc/clients/"+cid, headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"]})
    with urllib.request.urlopen(req, timeout=15) as response:
        identities = json.load(response)["credentials"]["federatedIdentities"]
    assert len(identities) == 1 and identities[0]["issuer"] == ISSUER, "unexpected federated identities"
    return identities[0]


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
 federated_identities = [{ issuer = "'''+ISSUER+'''", subject = "upgrade" }]
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
    #    replayProtection, so the server stores the identity with it disabled.
    config(released_version, "upgrade-fixture")
    run("init", "-input=false")
    run("apply", "-auto-approve", "-input=false")
    secret = state()["client_secret"]
    assert secret and "replay_protection" not in state()["federated_identities"][0], "released provider state is not the expected shape"
    before = server_identity()
    assert before.get("replayProtection", False) is False

    # 2. The new build takes over the same state and must plan nothing.
    config(DEV_VERSION, "upgrade-fixture")
    run("init", "-upgrade", "-input=false")
    run("plan", "-detailed-exitcode", "-input=false")  # exit 2 would mean a planned change
    run("apply", "-refresh-only", "-auto-approve", "-input=false")
    after = state()
    assert after["id"] == cid and after["client_secret"] == secret, "upgrade changed identity or secret"
    assert after["federated_identities"][0]["replay_protection"] is False, "refresh did not record the server's value"
    assert server_identity() == before, "upgrade changed the server's identity"

    # 3. An unrelated update replaces the whole identity list on the server. The
    #    omitted replay_protection must come through unchanged, not be re-defaulted.
    config(DEV_VERSION, "upgrade-fixture-renamed")
    run("apply", "-auto-approve", "-input=false")
    assert state()["client_secret"] == secret, "update changed the secret"
    assert server_identity() == before, "an unrelated update changed replay protection"
    run("plan", "-detailed-exitcode", "-input=false")

    run("destroy", "-auto-approve", "-input=false")
    print("PASS native "+tool+" upgrade "+released_version+" -> new build: empty plan, identity/secret kept, replay protection untouched")
