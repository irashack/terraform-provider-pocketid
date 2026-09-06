#!/usr/bin/env python3
"""Exercise a versioned provider installed from a native filesystem mirror.
Run only as a child of scripts/disposable-pocketid.py.
"""
import json
import secrets
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import uuid

tool, mirror = sys.argv[1:]
version = os.environ["POCKETID_TEST_VERSION"]
base = os.environ["POCKETID_BASE_URL"]
assert base.startswith("http://127.0.0.1:")
migration = os.environ.get("PROVIDER_MIGRATION_TEST") == "1"
source = "registry.opentofu.org/trozz/pocketid" if migration else "registry.terraform.io/irashack/pocketid"
provider_version = "2.3.0" if migration else "2.3.1"
cid = "native-" + uuid.uuid4().hex[:12]

def api(path, data=None, headers=None):
    req = urllib.request.Request(base+path, data=data, headers=headers or {"X-API-KEY":os.environ["POCKETID_API_TOKEN"]})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            payload = r.read()
            return r.status, json.loads(payload) if payload else None
    except urllib.error.HTTPError as e:
        payload = e.read()
        try: body=json.loads(payload)
        except ValueError: body={}
        return e.code, body

def check_secret(secret):
    if version == "2.14.0":
        code, metadata = api("/api/oidc/clients/"+cid+"/secrets")
        assert code == 200 and len(metadata) == 1, "expected one secret"
        values={"grant_type":"client_credentials","client_id":cid,"client_secret":secret}
        code, token = api("/api/oidc/token",urllib.parse.urlencode(values).encode(),{"Content-Type":"application/x-www-form-urlencoded"})
        assert code==200 and token.get("access_token"), "client authentication failed"
        values["client_secret"]="deliberately-invalid-secret"
        code, result = api("/api/oidc/token",urllib.parse.urlencode(values).encode(),{"Content-Type":"application/x-www-form-urlencoded"})
        assert code in (400,401) and result.get("error")=="invalid_client", "invalid secret accepted"

with tempfile.TemporaryDirectory(prefix="pocketid-native-") as tmp:
    root=Path(tmp)
    rc=root/"provider.rc"
    rc.write_text('provider_installation {\n filesystem_mirror {\n path = '+json.dumps(str(Path(mirror).resolve()))+'\n include = ["registry.terraform.io/irashack/pocketid", "registry.opentofu.org/trozz/pocketid"]\n }\n}\n')
    env=dict(os.environ, TF_CLI_CONFIG_FILE=str(rc), TF_IN_AUTOMATION="1")
    for key in list(env):
        if key.startswith("TF_LOG") or key in ("TF_PLUGIN_CACHE_DIR","TF_REATTACH_PROVIDERS"):del env[key]
    if tool == "tofu":
        env["TF_VAR_test_passphrase"] = secrets.token_urlsafe(32)
    def config(name):
        (root/"main.tf").write_text('''terraform {
 required_providers {
  pocketid = {
   source = "'''+source+'''"
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
}
''')
        if tool == "tofu":
            (root/"encryption.tf").write_text('''variable "test_passphrase" {
 type = string
 sensitive = true
}
terraform {
 encryption {
 key_provider "pbkdf2" "fixture" {
 passphrase = var.test_passphrase
 }
 method "aes_gcm" "fixture" {
 keys = key_provider.pbkdf2.fixture
 }
 state {
 method = method.aes_gcm.fixture
 enforced = true
 }
 plan {
 method = method.aes_gcm.fixture
 enforced = true
 }
 }
}
''')
    def assert_encrypted():
        for path in root.glob("terraform.tfstate*"):
            if path.is_file():
                body=json.loads(path.read_bytes())
                assert "encrypted_data" in body and "resources" not in body, "plaintext state/backup"
    def run(*args, codes=(0,)):
        result=subprocess.run([tool,*args],cwd=root,env=env,capture_output=True)
        assert result.returncode in codes, "native command failed: "+args[0]
        return result.stdout
    def state():
        obj=json.loads(run("show","-json"))
        resources=obj.get("values",{}).get("root_module",{}).get("resources",[])
        return resources[0]["values"] if resources else None
    config("native-fixture")
    run("init","-input=false")
    run("apply","-auto-approve","-input=false")
    first=state(); assert first["id"]==cid and first["client_secret"], "missing client/secret"
    secret=first["client_secret"]
    check_secret(secret)
    if tool == "tofu":
        assert_encrypted()
        run("plan","-out=fixture.tfplan","-input=false")
        saved=(root/"fixture.tfplan").read_bytes()
        assert not saved.startswith(b"PK") and secret.encode() not in saved, "plaintext saved plan"
        run("show","-json","fixture.tfplan")
        (root/"fixture.tfplan").unlink()
    if migration:
        assert tool == "tofu"
        run("state","replace-provider","-auto-approve",source,"registry.terraform.io/irashack/pocketid")
        assert_encrypted()
        source="registry.terraform.io/irashack/pocketid"
        provider_version="2.3.1"
        config("native-fixture")
        run("init","-input=false")
        assert state()["id"]==cid and state()["client_secret"]==secret, "migration changed identity/secret"
    run("plan","-detailed-exitcode","-input=false")
    run("apply","-refresh-only","-auto-approve","-input=false")
    assert state()["client_secret"]==secret,"refresh changed secret"
    config("native-fixture-updated")
    run("apply","-auto-approve","-input=false")
    assert state()["id"]==cid and state()["client_secret"]==secret,"update changed identity/secret"
    check_secret(secret)
    run("state","rm","pocketid_client.test")
    run("import","-input=false","pocketid_client.test",cid)
    assert not state().get("client_secret"),"import invented secret"
    check_secret(secret)
    # Imported client_id is optional metadata; the configured value can be
    # reconciled through an ordinary update, without rotating its secret.
    run("apply","-auto-approve","-input=false")
    run("plan","-detailed-exitcode","-input=false")
    check_secret(secret)
    run("destroy","-auto-approve","-input=false")
    assert api("/api/oidc/clients/"+cid)[0]==404,"delete did not remove fixture"
    if tool == "tofu": assert_encrypted()
    print("PASS native "+tool+" Pocket ID "+version+": install/create/refresh/update/import/empty-plan/delete" + ("/single-secret/authentication" if version=="2.14.0" else "") + ("/encrypted-state-and-plan" if tool=="tofu" else "") + ("/provider-address-migration" if migration else ""))
