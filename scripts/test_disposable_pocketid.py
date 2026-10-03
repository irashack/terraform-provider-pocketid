"""Unit tests for the fixture's failure-log handling and host reachability
(no container needed).

Run: python3 -m unittest discover -s scripts -p 'test_*.py'
"""
import importlib.util
import os
import stat
import tempfile
import unittest

_spec = importlib.util.spec_from_file_location(
    "disposable_pocketid", os.path.join(os.path.dirname(os.path.abspath(__file__)), "disposable-pocketid.py"))
fixture = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fixture)


class FailureLogTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.path = os.path.join(self.dir.name, "failure.log")

    def test_created_with_mode_0600(self):
        old = os.umask(0o022)
        try:
            fixture.check_failure_log_path(self.path)
            fixture.write_failure_log(self.path, b"raw output")
        finally:
            os.umask(old)
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        with open(self.path, "rb") as f:
            self.assertEqual(f.read(), b"raw output")

    def test_existing_file_refused_and_untouched(self):
        with open(self.path, "wb") as f:
            f.write(b"keep")
        os.chmod(self.path, 0o644)
        with self.assertRaises(ValueError):
            fixture.check_failure_log_path(self.path)
        with self.assertRaises(FileExistsError):
            fixture.write_failure_log(self.path, b"raw output")
        with open(self.path, "rb") as f:
            self.assertEqual(f.read(), b"keep")
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o644)

    def test_symlink_refused_and_target_untouched(self):
        target = os.path.join(self.dir.name, "target")
        with open(target, "wb") as f:
            f.write(b"keep")
        os.symlink(target, self.path)
        with self.assertRaises(ValueError):
            fixture.check_failure_log_path(self.path)
        with self.assertRaises(OSError):
            fixture.write_failure_log(self.path, b"raw output")
        with open(target, "rb") as f:
            self.assertEqual(f.read(), b"keep")

    def test_dangling_symlink_refused_and_nothing_created(self):
        target = os.path.join(self.dir.name, "would-be-created")
        os.symlink(target, self.path)
        with self.assertRaises(ValueError):
            fixture.check_failure_log_path(self.path)
        with self.assertRaises(OSError):
            fixture.write_failure_log(self.path, b"raw output")
        self.assertFalse(os.path.exists(target))

    def test_directory_refused(self):
        os.mkdir(self.path)
        with self.assertRaises(ValueError):
            fixture.check_failure_log_path(self.path)

    def test_missing_directory_refused(self):
        with self.assertRaises(ValueError):
            fixture.check_failure_log_path(os.path.join(self.dir.name, "missing", "failure.log"))


class HostReachTest(unittest.TestCase):
    def test_linux_adds_the_host_name_and_binds_every_interface(self):
        extra, bind = fixture.host_reach("linux")
        self.assertEqual(extra, ["--add-host", "host.docker.internal:host-gateway"])
        self.assertEqual(bind, "0.0.0.0")

    def test_macos_keeps_the_runtimes_own_name_and_stays_on_loopback(self):
        extra, bind = fixture.host_reach("darwin")
        self.assertEqual(extra, [])
        self.assertEqual(bind, "127.0.0.1")

    def test_run_command_carries_the_host_name_only_on_linux(self):
        linux = fixture.docker_run_command("name", "/tmp/env", "2.14.0", "linux")
        darwin = fixture.docker_run_command("name", "/tmp/env", "2.14.0", "darwin")
        pair = ["--add-host", "host.docker.internal:host-gateway"]
        self.assertEqual(linux[linux.index("--add-host"):linux.index("--add-host") + 2], pair)
        self.assertNotIn("--add-host", darwin)
        for command in (linux, darwin):
            self.assertEqual(command[-1], "ghcr.io/pocket-id/pocket-id:v2.14.0")
            self.assertIn("127.0.0.1::1411", command)  # still published on loopback only


if __name__ == "__main__":
    unittest.main()
