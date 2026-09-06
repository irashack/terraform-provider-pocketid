#!/usr/bin/env python3
"""Disposable official-image fixture. Seeds only its fresh SQLite database.

Usage: disposable-pocketid.py VERSION -- COMMAND [ARGS...]
Credentials exist only in this process and child environment. No production URL
or database input is accepted. The temporary container is removed on exit.
"""
import hashlib
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid


def run(version, command):
    if version not in ("2.9.0", "2.13.0", "2.14.0"):
        raise ValueError("version must be in the tested matrix")
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="pocketid-fixture-") as tmp:
        root = Path(tmp)
        data = root / "data"
        data.mkdir()
        token = secrets.token_urlsafe(32)
        name = "provider-fixture-" + uuid.uuid4().hex[:12]
        envfile = root / "container.env"
        envfile.write_text("APP_URL=http://localhost:1411\nENCRYPTION_KEY=" + secrets.token_hex(32) +
                           "\nTRUST_PROXY=false\nVERSION_CHECK_DISABLED=true\nANALYTICS_DISABLED=true\nPUID=" + str(os.getuid()) +
                           "\nPGID=" + str(os.getgid()) + "\n")
        created = False
        try:
            subprocess.run(["docker", "run", "-d", "--name", name, "--label", "pocketid-provider-fixture=true",
                            "--env-file", str(envfile), "-p", "127.0.0.1::1411",
                            "ghcr.io/pocket-id/pocket-id:v"+version], check=True, capture_output=True)
            created = True
            port = subprocess.check_output(["docker", "port", name, "1411/tcp"], text=True).strip().split(":")[-1]
            base = "http://127.0.0.1:" + port
            print("Fixture listener " + base, flush=True)
            dbpath = data / "pocket-id.db"
            for _ in range(120):
                try:
                    with urllib.request.urlopen(base+"/healthz", timeout=1) as response:
                        if 200 <= response.status < 300:
                            break
                except Exception as exc:
                    last_health_error = type(exc).__name__ + ":" + str(getattr(exc, "code", ""))
                time.sleep(0.5)
            else:
                raise RuntimeError("fixture health check did not become ready: " + last_health_error)
            # Stop before SQLite access: never access a live WAL across the VM
            # shared-filesystem boundary. Seeding is fixture-only bootstrap.
            subprocess.run(["docker", "stop", name], check=True, capture_output=True)
            subprocess.run(["docker", "cp", name+":/app/data/.", str(data)], check=True, capture_output=True)
            with sqlite3.connect(dbpath) as db:
                # Inspect schema before seeding; never adapt against arbitrary data.
                cols = {r[1] for r in db.execute("PRAGMA table_info(users)")}
                if not {"id", "username", "is_admin", "email"} <= cols:
                    raise RuntimeError("unsupported fixture schema")
                if db.execute("SELECT count(*) FROM users").fetchone()[0] != 0:
                    raise RuntimeError("unexpected existing users in fresh fixture")
                uid = str(uuid.uuid4())
                db.execute("INSERT INTO users (id,email,username,first_name,last_name,display_name,is_admin,disabled,created_at) VALUES (?,?,?,?,?,?,1,0,datetime('now'))",
                           (uid,"fixture@example.invalid","fixture-admin","Fixture","Admin","Fixture Admin"))
                db.execute("INSERT INTO api_keys (id,key,user_id,name,created_at,expires_at) VALUES (?,?,?,?,datetime('now'),datetime('now','+1 day'))",
                           (str(uuid.uuid4()),hashlib.sha256(token.encode()).hexdigest(),uid,"provider-fixture"))
            subprocess.run(["docker", "cp", str(data)+"/.", name+":/app/data"], check=True, capture_output=True)
            subprocess.run(["docker", "start", name], check=True, capture_output=True)
            port = subprocess.check_output(["docker", "port", name, "1411/tcp"], text=True).strip().split(":")[-1]
            base = "http://127.0.0.1:" + port
            for _ in range(60):
                try:
                    with urllib.request.urlopen(base+"/healthz", timeout=1):
                        break
                except Exception:
                    time.sleep(0.5)
            req = urllib.request.Request(base+"/api/version/current", headers={"X-API-KEY":token})
            with urllib.request.urlopen(req,timeout=10) as response:
                import json
                if json.load(response).get("currentVersion", "").lstrip("v") != version:
                    raise RuntimeError("running version mismatch")
            env = dict(os.environ)
            for key in list(env):
                if key.startswith(("TF_LOG", "POCKETID_")):
                    del env[key]
            env.update(POCKETID_BASE_URL=base, POCKETID_API_TOKEN=token, TF_ACC="1", POCKETID_TEST_VERSION=version)
            print("Fixture " + version + " ready (official image, isolated database, loopback)", flush=True)
            result = subprocess.run(command, env=env, capture_output=True)
            output = result.stdout + result.stderr
            # Test tooling can print state on failure. Redact credentials in memory,
            # and emit only test names/status, never raw command output.
            for line in output.decode(errors="replace").splitlines():
                if line.startswith(("ok\t", "FAIL\t", "--- PASS:", "--- FAIL:", "PASS", "FAIL")):
                    print(line)
            if result.returncode:
                (root / "failure.log").write_bytes(output)
                # Optional private log destination for local diagnosis, outside git.
                if os.environ.get("POCKETID_FIXTURE_FAILURE_LOG"):
                    Path(os.environ["POCKETID_FIXTURE_FAILURE_LOG"]).write_bytes(output)
                raise RuntimeError("fixture command failed; raw output suppressed")
            print("Fixture " + version + " command PASS", flush=True)
        finally:
            if created:
                subprocess.run(["docker", "rm", "-fv", name], check=True, capture_output=True)
                print("Fixture container removed", flush=True)

if __name__ == "__main__":
    try:
        if len(sys.argv) < 4 or sys.argv[2] != "--":
            raise ValueError(__doc__)
        run(sys.argv[1], sys.argv[3:])
    except Exception as exc:
        print(type(exc).__name__ + ": " + (str(exc) if isinstance(exc, (RuntimeError, ValueError, sqlite3.Error)) else "fixture failed (details withheld)"), file=sys.stderr)
        sys.exit(1)
