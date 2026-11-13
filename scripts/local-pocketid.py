#!/usr/bin/env python3
"""Local diagnosis fixture: Pocket ID's release binary on loopback, no container.

Usage: local-pocketid.py VERSION -- COMMAND [ARGS...]

For a host without a container runtime, and for reading a failure that
disposable-pocketid.py only summarizes: the command's output is passed through
whole. Test tooling can print state and credentials when it fails, so never run
this in CI or paste its output into a public place; the credentials are
disposable and die with the process, the habit should not form.

The binary is Pocket ID's own release asset for this platform, checked against
that release's checksums.txt and kept under the user cache directory. The server
gets a fresh SQLite database in a temporary directory, seeded with one admin and
one API key, and listens on a free loopback port. The command runs with the same
environment disposable-pocketid.py gives (POCKETID_BASE_URL, POCKETID_API_TOKEN,
POCKETID_TEST_VERSION, TF_ACC=1, POCKETID_TEST_HOST_BIND=127.0.0.1).

Not covered here: TestAccResourceScimSync advertises host.docker.internal, which
only a container runtime defines (skip it with `-skip TestAccResourceScimSync`).
The acceptance tests need a terraform on PATH or TF_ACC_TERRAFORM_PATH (TESTING.md).
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

RELEASES = "https://github.com/pocket-id/pocket-id/releases/download/v"
SYSTEMS = {"Linux": "linux", "Darwin": "darwin", "FreeBSD": "freebsd"}
MACHINES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}


def asset_name(system, machine):
    if system not in SYSTEMS or machine not in MACHINES:
        raise ValueError("no Pocket ID release binary is known for " + system + " " + machine)
    return "pocket-id_" + SYSTEMS[system] + "_" + MACHINES[machine]


def expected_digest(checksums, asset):
    """The SHA-256 a checksums.txt names for one asset; the name must match
    exactly, so `pocket-id_linux_arm` never takes `pocket-id_linux_arm64`'s."""
    for line in checksums.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1].lstrip("*") == asset and re.fullmatch(r"[0-9a-f]{64}", parts[0]):
            return parts[0]
    raise RuntimeError("checksums.txt does not name " + asset)


def cache_root():
    return Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "pocketid-provider-fixture"


def fetch(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read()


def binary(version):
    asset = asset_name(platform.system(), platform.machine())
    target = cache_root() / version / asset
    if target.exists():
        return target
    base = RELEASES + version + "/"
    want = expected_digest(fetch(base + "checksums.txt").decode(), asset)
    body = fetch(base + asset)
    if hashlib.sha256(body).hexdigest() != want:
        raise RuntimeError("the downloaded " + asset + " does not match checksums.txt")
    target.parent.mkdir(parents=True, exist_ok=True)
    partial = target.with_name(asset + ".partial-" + uuid.uuid4().hex[:8])
    partial.write_bytes(body)
    partial.chmod(0o700)
    partial.replace(target)
    return target


def free_port():
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def seed(dbpath, token):
    with sqlite3.connect(dbpath) as db:
        cols = {r[1] for r in db.execute("PRAGMA table_info(users)")}
        if not {"id", "username", "is_admin", "email"} <= cols:
            raise RuntimeError("unsupported fixture schema")
        if db.execute("SELECT count(*) FROM users").fetchone()[0] != 0:
            raise RuntimeError("unexpected existing users in fresh fixture")
        uid = str(uuid.uuid4())
        db.execute("INSERT INTO users (id,email,username,first_name,last_name,display_name,is_admin,disabled,created_at) VALUES (?,?,?,?,?,?,1,0,datetime('now'))",
                   (uid, "fixture@example.invalid", "fixture-admin", "Fixture", "Admin", "Fixture Admin"))
        db.execute("INSERT INTO api_keys (id,key,user_id,name,created_at,expires_at) VALUES (?,?,?,?,datetime('now'),datetime('now','+1 day'))",
                   (str(uuid.uuid4()), hashlib.sha256(token.encode()).hexdigest(), uid, "provider-fixture"))


def run(version, command):
    if not re.fullmatch(r"\d+\.\d+\.\d+", version):
        raise ValueError("VERSION must be X.Y.Z")
    os.umask(0o077)
    server_binary = binary(version)
    with tempfile.TemporaryDirectory(prefix="pocketid-local-") as tmp:
        root = Path(tmp)
        (root / "data").mkdir()
        token = secrets.token_urlsafe(32)
        port = str(free_port())
        base = "http://127.0.0.1:" + port
        server_env = {"PATH": os.environ.get("PATH", ""), "HOME": tmp, "APP_URL": "http://localhost:1411",
                      "ENCRYPTION_KEY": secrets.token_hex(32), "TRUST_PROXY": "false",
                      "VERSION_CHECK_DISABLED": "true", "ANALYTICS_DISABLED": "true",
                      "OUTBOUND_ALLOWED_HOSTS_SCIM": "private,loopback", "HOST": "127.0.0.1", "PORT": port}

        def start():
            log = open(root / "server.log", "ab")
            server = subprocess.Popen([str(server_binary)], cwd=root, env=server_env, stdout=log, stderr=subprocess.STDOUT)
            for _ in range(120):
                if server.poll() is not None:
                    break
                try:
                    with urllib.request.urlopen(base + "/healthz", timeout=1) as response:
                        if 200 <= response.status < 300:
                            return server
                except Exception:
                    time.sleep(0.5)
            server.kill()
            server.wait()
            raise RuntimeError("the server did not become ready; its log is kept at " + keep_log(root))

        server = start()
        server.terminate()
        server.wait()
        found = list(root.rglob("pocket-id.db"))
        if len(found) != 1:
            raise RuntimeError("expected one fresh pocket-id.db")
        seed(found[0], token)
        server = start()
        try:
            request = urllib.request.Request(base + "/api/version/current", headers={"X-API-KEY": token})
            with urllib.request.urlopen(request, timeout=10) as response:
                if json.load(response).get("currentVersion", "").lstrip("v") != version:
                    raise RuntimeError("running version mismatch")
            env = {key: value for key, value in os.environ.items() if not key.startswith(("TF_LOG", "POCKETID_"))}
            env.update(POCKETID_BASE_URL=base, POCKETID_API_TOKEN=token, TF_ACC="1",
                       POCKETID_TEST_VERSION=version, POCKETID_TEST_HOST_BIND="127.0.0.1")
            print("Local fixture " + version + " ready at " + base + " (release binary, fresh database, loopback)", file=sys.stderr, flush=True)
            return subprocess.run(command, env=env).returncode
        finally:
            server.terminate()
            try:
                server.wait(timeout=20)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


def keep_log(root):
    """Copy the server log out of the temporary directory, mode 0600."""
    fd, kept = tempfile.mkstemp(prefix="pocketid-local-server-", suffix=".log")
    os.close(fd)
    shutil.copyfile(root / "server.log", kept)
    return kept


if __name__ == "__main__":
    try:
        if len(sys.argv) < 4 or sys.argv[2] != "--":
            raise ValueError(__doc__)
        sys.exit(run(sys.argv[1], sys.argv[3:]))
    except (RuntimeError, ValueError, OSError, sqlite3.Error) as exc:
        print(type(exc).__name__ + ": " + str(exc), file=sys.stderr)
        sys.exit(1)
