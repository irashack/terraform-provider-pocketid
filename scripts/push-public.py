#!/usr/bin/env python3
"""Push to the public GitHub repository, and only what belongs there.

Usage: push-public.py [--dry-run] NAME [NAME...]

This repository is developed private-first: the private remote is the source
of truth and runs the acceptance suites; the public repository receives what
was merged there. A ref pushed to a public repository cannot be taken back, so
this is the only way a ref should reach it, and it refuses rather than guesses.

What may be pushed (anything else is refused, as is any refspec, deletion or
force):

- the branch `main-maintenance`, at exactly the private remote's commit. The
  local branch is not consulted, so unmerged local work cannot leave.
- a release tag `vX.Y.Z` that the private remote has, as the same tag object,
  on a commit of the private `main-maintenance`. A tag the public repository
  already has is never moved.

The private remote is the checkout's one remote that is not on github.com; with
none or several, nothing is pushed. The token comes from GH_TOKEN and reaches
git through its environment, never argv or the repository's config.
"""
import base64
import os
import re
import subprocess
import sys

PUBLIC_URL = "https://github.com/irashack/terraform-provider-pocketid.git"
BRANCHES = re.compile(r"main-maintenance")
TAGS = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)")
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]*")


class Refused(Exception):
    pass


def classify(name):
    """'branch' or 'tag' for a name the public repository may receive."""
    if not NAME.fullmatch(name) or ".." in name or name.endswith(("/", ".", ".lock")):
        raise Refused("'" + name + "' is not a plain branch or tag name (no refspecs, options or deletions)")
    if BRANCHES.fullmatch(name):
        return "branch"
    if TAGS.fullmatch(name):
        return "tag"
    raise Refused("'" + name + "' may not go to the public repository: only main-maintenance and vX.Y.Z tags do")


def is_github(url):
    return re.match(r"(https://|ssh://git@|git@)github\.com[:/]", url) is not None


def private_remote(remotes):
    """remotes maps name to URL. The one remote that is not on github.com."""
    private = sorted(name for name, url in remotes.items() if not is_github(url))
    if len(private) != 1:
        raise Refused("expected exactly one remote that is not on github.com, found " + (", ".join(private) or "none"))
    return private[0]


def git(*args, env=None):
    result = subprocess.run(["git", *args], capture_output=True, text=True, env=env)
    if result.returncode:
        raise Refused("git " + args[0] + " failed: " + result.stderr.strip().splitlines()[-1] if result.stderr.strip() else "git " + args[0] + " failed")
    return result.stdout


def ls_remote(remote, ref, env=None):
    """{refname: object} for one ref of a remote, peeled entry included."""
    out = git("ls-remote", remote, ref, ref + "^{}", env=env)
    return {line.split("\t")[1]: line.split("\t")[0] for line in out.splitlines() if "\t" in line}


def public_env():
    token = os.environ.get("GH_TOKEN")
    if not token:
        raise Refused("GH_TOKEN is not set")
    header = "Authorization: Basic " + base64.b64encode(("x-access-token:" + token).encode()).decode()
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0", GIT_CONFIG_COUNT="2",
               GIT_CONFIG_KEY_0="http.extraHeader", GIT_CONFIG_VALUE_0=header,
               GIT_CONFIG_KEY_1="credential.helper", GIT_CONFIG_VALUE_1="")
    return env


def plan(names):
    """The refspecs to push, after every check. Nothing is pushed here."""
    kinds = [(name, classify(name)) for name in names]
    remotes = {}
    for line in git("remote", "-v").splitlines():
        name, url, direction = line.split()
        if direction == "(fetch)":
            remotes[name] = url
    private = private_remote(remotes)
    env = public_env()
    main = ls_remote(private, "refs/heads/main-maintenance").get("refs/heads/main-maintenance")
    if not main:
        raise Refused("the private remote has no main-maintenance")
    git("fetch", "--quiet", private, "refs/heads/main-maintenance")
    refspecs = []
    for name, kind in kinds:
        if kind == "branch":
            if ls_remote(PUBLIC_URL, "refs/heads/" + name, env=env).get("refs/heads/" + name) != main:
                refspecs.append(main + ":refs/heads/" + name)
            continue
        ref = "refs/tags/" + name
        there = ls_remote(private, ref)
        if ref not in there:
            raise Refused(name + " is not on the private remote: push it there first")
        if ref + "^{}" not in there:
            raise Refused(name + " is a lightweight tag; a release tag is annotated")
        local = git("rev-parse", "--verify", "--quiet", ref).strip()
        if local != there[ref]:
            raise Refused(name + " differs between this checkout and the private remote")
        commit = there[ref + "^{}"]
        if subprocess.run(["git", "merge-base", "--is-ancestor", commit, main]).returncode:
            raise Refused(name + " is not on the private main-maintenance")
        public = ls_remote(PUBLIC_URL, ref, env=env)
        if ref in public and public[ref] != there[ref]:
            raise Refused(name + " already exists on the public repository as a different tag; a released tag is never moved")
        if ref not in public:
            refspecs.append(ref + ":" + ref)
    return refspecs, env


def main(argv):
    dry_run = argv[:1] == ["--dry-run"]
    names = argv[1:] if dry_run else argv
    if not names:
        raise Refused(__doc__)
    refspecs, env = plan(names)
    if not refspecs:
        print("nothing to push: the public repository already has it")
        return
    for refspec in refspecs:
        print(("would push " if dry_run else "pushing ") + refspec)
    if not dry_run:
        # No --force, no --tags, no --mirror: a fast-forward or a new tag only.
        result = subprocess.run(["git", "push", "--no-follow-tags", PUBLIC_URL, *refspecs], env=env)
        if result.returncode:
            raise Refused("git push failed")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except Refused as refusal:
        print("push-public: " + str(refusal), file=sys.stderr)
        sys.exit(1)
