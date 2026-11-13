"""Unit tests for local-pocketid.py and push-public.py (no network, no git).

Run: python3 -m unittest discover -s scripts -p 'test_*.py'
"""
import importlib.util
import os
import unittest


def load(name, filename):
    spec = importlib.util.spec_from_file_location(
        name, os.path.join(os.path.dirname(os.path.abspath(__file__)), filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


local = load("local_pocketid", "local-pocketid.py")
push = load("push_public", "push-public.py")


class ReleaseBinaryTest(unittest.TestCase):
    def test_asset_name(self):
        self.assertEqual(local.asset_name("Linux", "x86_64"), "pocket-id_linux_amd64")
        self.assertEqual(local.asset_name("Darwin", "arm64"), "pocket-id_darwin_arm64")
        with self.assertRaises(ValueError):
            local.asset_name("Plan9", "x86_64")

    def test_digest_is_taken_for_the_exact_name(self):
        sums = "a" * 64 + "  pocket-id_linux_arm64\n" + "b" * 64 + "  pocket-id_linux_amd64\n"
        self.assertEqual(local.expected_digest(sums, "pocket-id_linux_amd64"), "b" * 64)
        with self.assertRaises(RuntimeError):
            local.expected_digest(sums, "pocket-id_linux_arm")

    def test_a_malformed_digest_is_not_accepted(self):
        with self.assertRaises(RuntimeError):
            local.expected_digest("abc  pocket-id_linux_amd64\n", "pocket-id_linux_amd64")


class PublicRefTest(unittest.TestCase):
    def test_what_may_be_public(self):
        self.assertEqual(push.classify("main-maintenance"), "branch")
        self.assertEqual(push.classify("v3.2.2"), "tag")
        self.assertEqual(push.classify("v10.0.0"), "tag")

    def test_everything_else_is_refused(self):
        for name in ("main", "fix/secret", "main-maintenance-2", "v3.2.2-rc.1", "v3.2", "v03.2.1",
                     "+main-maintenance", "main-maintenance:main", ":main-maintenance", "--force",
                     "-d", "refs/heads/main-maintenance", "", "v1.2.3^{}", "a..b", "HEAD"):
            with self.assertRaises(push.Refused, msg=name):
                push.classify(name)

    def test_the_private_remote_is_the_one_not_on_github(self):
        github = "https://github.com/irashack/terraform-provider-pocketid.git"
        self.assertEqual(push.private_remote({"origin": github, "private": "https://git.example.invalid/x/y.git"}), "private")
        with self.assertRaises(push.Refused):
            push.private_remote({"origin": github})
        with self.assertRaises(push.Refused):
            push.private_remote({"a": "https://one.example.invalid/x.git", "b": "https://two.example.invalid/x.git"})

    def test_github_urls(self):
        for url in ("https://github.com/o/r.git", "git@github.com:o/r.git", "ssh://git@github.com/o/r"):
            self.assertTrue(push.is_github(url), url)
        for url in ("https://github.com.example.invalid/o/r.git", "https://git.example.invalid/github.com/r"):
            self.assertFalse(push.is_github(url), url)


if __name__ == "__main__":
    unittest.main()
