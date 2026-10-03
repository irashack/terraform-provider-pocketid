#!/usr/bin/env python3
"""SMTP-only native lifecycle against disposable-pocketid.py; synthetic data only.

Usage: application_config.py TOOL MIRROR TARGET_VERSION [OLD_VERSION]

With OLD_VERSION, the older provider imports the configuration and manages the
SMTP settings with the plain smtp_password, as a configuration written for it
would; the target version then takes over that state and must plan nothing for
the unchanged configuration. Either way the target version then moves the
password to smtp_password_wo, which must keep it out of state and plan without
blanking it on the server, and keep it through an update without a refresh.
"""
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

tool, mirror, target_version = sys.argv[1:4]
provider_version = sys.argv[4] if len(sys.argv) == 5 else target_version
upgrading = provider_version != target_version
version = os.environ["POCKETID_TEST_VERSION"]
base = os.environ["POCKETID_BASE_URL"]
assert base.startswith("http://127.0.0.1:")
os.umask(0o077)


def api(method, config=None):
    path = "/api/application-configuration" + ("/all" if method == "GET" else "")
    request = urllib.request.Request(base + path, method=method,
        headers={"X-API-KEY": os.environ["POCKETID_API_TOKEN"], "Content-Type": "application/json"},
        data=json.dumps(config).encode() if config is not None else None)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, {v["key"]: v["value"] for v in json.load(response)}
    except urllib.error.HTTPError as error:
        error.read()
        return error.code, None


code, original = api("GET")
assert code == 200
new_fields = {
    "webauthnUserVerification": "required",
    "webauthnAllowSyncedPasskeys": "false",
    "webauthnAuthenticatorAttachment": "cross-platform",
    "cimdUrlAllowlist": '["https://trusted.example.invalid/client.json"]',
}
# Reproduce the old provider omission: the actual upstream validator
# must reject it, with no side effects, before testing the fixed binary.
legacy = {key: value for key, value in original.items() if key not in new_fields}
legacy["smtpHost"] = "legacy.example.invalid"
assert api("PUT", legacy)[0] == 400, "old payload unexpectedly accepted"
assert api("GET")[1] == original, "rejected request changed configuration"
original.update(new_fields)
# Seed nondefault unrelated settings through the disposable server's native API.
original.update(appName="SMTP fixture", sessionDuration="47", allowUserSignups="withToken",
                signupDefaultCustomClaims='[{"key":"fixture","value":"retained"}]',
                smtpPassword=secrets.token_urlsafe(24), ldapBindPassword=secrets.token_urlsafe(24))
assert api("PUT", original)[0] == 200, "fixture seed rejected"
original = api("GET")[1]

with tempfile.TemporaryDirectory(prefix="pocketid-smtp-native-") as tmp:
    root = Path(tmp)
    rc = root / "provider.rc"
    rc.write_text('provider_installation {\n filesystem_mirror {\n path = ' +
        json.dumps(str(Path(mirror).resolve())) +
        '\n include = ["registry.terraform.io/irashack/pocketid"]\n }\n}\n')
    env = dict(os.environ, TF_CLI_CONFIG_FILE=str(rc), TF_IN_AUTOMATION="1")
    for key in list(env):
        if key.startswith("TF_LOG") or key in ("TF_PLUGIN_CACHE_DIR", "TF_REATTACH_PROVIDERS"):
            del env[key]
    if tool == "tofu":
        env["TF_VAR_test_passphrase"] = secrets.token_urlsafe(32)
        (root / "encryption.tf").write_text('''variable "test_passphrase" {
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
    def config(smtp):
        (root / "main.tf").write_text('''terraform {
 required_providers {
  pocketid = {
   source = "registry.terraform.io/irashack/pocketid"
   version = "''' + provider_version + '''"
  }
 }
}
provider "pocketid" {}
resource "pocketid_application_config" "test" {
''' + ''.join(' ' + key + ' = ' + json.dumps(value) + '\n' for key, value in smtp.items()) + '''}
data "pocketid_application_config" "test" {
 depends_on = [pocketid_application_config.test]
}
''')
    def run(*args, ok=(0,)):
        result = subprocess.run([tool, *args], cwd=root, env=env, capture_output=True)
        assert result.returncode in ok, "native command failed: " + args[0]
        return result.stdout
    def verify(expected, secrets):
        """secrets maps smtp_password and ldap_bind_password to the value the
        resource must hold in state: None when it is not tracked."""
        assert api("GET")[1] == expected, "unrelated server setting changed"
        state = json.loads(run("show", "-json"))
        rows = state["values"]["root_module"]["resources"]
        assert len(rows) == 2
        for row in rows:
            values = row["values"]
            for attr, key in (("webauthn_user_verification", "webauthnUserVerification"),
                              ("webauthn_allow_synced_passkeys", "webauthnAllowSyncedPasskeys"),
                              ("webauthn_authenticator_attachment", "webauthnAuthenticatorAttachment"),
                              ("cimd_url_allowlist", "cimdUrlAllowlist")):
                assert values[attr] == expected.get(key, ""), "resource/data-source mapping mismatch: " + attr
            for attr in ("smtp_password", "ldap_bind_password"):
                if row["mode"] == "data":
                    assert attr not in values, "the data source exposes " + attr
                else:
                    assert values.get(attr) == secrets[attr], "state holds the wrong " + attr
                    assert values.get(attr + "_wo") is None, "state holds a write-only value"
        if tool == "terraform":
            # The current state only: a backup holds the state before the
            # last change, which may still have had the plain password.
            body = (root / "terraform.tfstate").read_bytes()
            for value in secrets.get("absent", ()):
                assert value.encode() not in body, "a password that must be out of state is in it"
        if tool == "tofu":
            for path in root.glob("terraform.tfstate*"):
                body = json.loads(path.read_bytes())
                assert "encrypted_data" in body and "resources" not in body, "plaintext state/backup"
    # The shape of the homelab's configuration: SMTP with the plain password,
    # plus the two account settings it manages.
    smtp = {"smtp_host": "smtp.fastmail.com", "smtp_port": "587", "smtp_tls": "starttls",
            "smtp_skip_cert_verify": "false", "smtp_from": "fixture@example.invalid",
            "smtp_user": "fixture@example.invalid", "smtp_password": secrets.token_urlsafe(24),
            "allow_own_account_edit": "false", "email_login_notification_enabled": "true"}
    keys = {"smtp_host": "smtpHost", "smtp_port": "smtpPort", "smtp_tls": "smtpTls",
            "smtp_skip_cert_verify": "smtpSkipCertVerify", "smtp_from": "smtpFrom", "smtp_user": "smtpUser",
            "smtp_password": "smtpPassword", "allow_own_account_edit": "allowOwnAccountEdit",
            "email_login_notification_enabled": "emailLoginNotificationEnabled"}
    expected = dict(original)
    expected.update({keys[attr]: value for attr, value in smtp.items()})
    config({})
    run("init", "-input=false")
    run("import", "-input=false", "pocketid_application_config.test", "application-configuration")
    if upgrading:
        # The older provider writes the state: it stored every password it
        # read, the LDAP one included, and manages SMTP with the plain input.
        config(smtp)
        run("apply", "-auto-approve", "-input=false")
        provider_version = target_version
        config(smtp)
        run("init", "-upgrade", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")  # unchanged configuration: no change
        # The data source's stored result still has the removed password
        # attributes, which "show" cannot decode until a refresh rewrites it.
        run("show", "-json", ok=(1,))
        run("apply", "-refresh-only", "-auto-approve", "-input=false")
        ldap_in_state = original["ldapBindPassword"]
    else:
        run("apply", "-refresh-only", "-auto-approve", "-input=false")
        run("plan", "-detailed-exitcode", "-input=false")
        # An import tracks no password: none is copied into state.
        verify(original, {"smtp_password": None, "ldap_bind_password": None})
        config(smtp)
        run("apply", "-auto-approve", "-input=false")
        ldap_in_state = None
    verify(expected, {"smtp_password": smtp["smtp_password"], "ldap_bind_password": ldap_in_state})
    run("apply", "-refresh-only", "-auto-approve", "-input=false")
    run("plan", "-detailed-exitcode", "-input=false")
    verify(expected, {"smtp_password": smtp["smtp_password"], "ldap_bind_password": ldap_in_state})

    # Move the password to the write-only input: the server gets the new
    # value, state and plan never hold it, and the plain value leaves state.
    write_only = {key: value for key, value in smtp.items() if key != "smtp_password"}
    write_only.update(smtp_password_wo=secrets.token_urlsafe(24), smtp_password_wo_version="1")
    config(write_only)
    run("apply", "-auto-approve", "-input=false")
    expected["smtpPassword"] = write_only["smtp_password_wo"]
    hidden = (smtp["smtp_password"], write_only["smtp_password_wo"])
    verify(expected, {"smtp_password": None, "ldap_bind_password": ldap_in_state, "absent": hidden})
    run("plan", "-detailed-exitcode", "-input=false")
    # An unrelated update applied without a refresh keeps the password.
    write_only["smtp_host"] = "smtp.example.invalid"
    config(write_only)
    run("apply", "-refresh=false", "-auto-approve", "-input=false")
    expected["smtpHost"] = write_only["smtp_host"]
    verify(expected, {"smtp_password": None, "ldap_bind_password": ldap_in_state, "absent": hidden})
    run("plan", "-detailed-exitcode", "-input=false")

    # Removing managed attributes inherits the existing server configuration
    # (dropping the write-only version is the one change to apply).
    config({})
    run("apply", "-auto-approve", "-input=false")
    run("plan", "-detailed-exitcode", "-input=false")
    verify(expected, {"smtp_password": None, "ldap_bind_password": ldap_in_state, "absent": hidden})
    run("state", "rm", "pocketid_application_config.test")
    run("import", "-input=false", "pocketid_application_config.test", "application-configuration")
    run("apply", "-refresh-only", "-auto-approve", "-input=false")
    run("plan", "-detailed-exitcode", "-input=false", "-out=fixture.tfplan")
    if tool == "tofu":
        body = (root / "fixture.tfplan").read_bytes()
        assert not body.startswith(b"PK") and all(value.encode() not in body for value in hidden)
        run("show", "-json", "fixture.tfplan")
    verify(expected, {"smtp_password": None, "ldap_bind_password": None, "absent": hidden + (original["ldapBindPassword"],)})
    run("destroy", "-auto-approve", "-input=false")
    assert api("GET")[1] == expected, "destroy changed live singleton"
    print("PASS native " + tool + " Pocket ID " + version + ": old-payload validation/import/SMTP preservation/data-source/refresh/empty-plan/" +
          "write-only password (kept on the server, out of state, kept by an unrefreshed update)/removal" +
          ("/encrypted-state-backups-plan" if tool == "tofu" else "") + ("/upgrade from " + sys.argv[4] + " with an empty plan" if upgrading else ""))
