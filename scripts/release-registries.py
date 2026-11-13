#!/usr/bin/env python3
"""Whether the Terraform and OpenTofu registries carry a release and its pages.

Usage: release-registries.py X.Y.Z      (exit 0 when both do, 2 when one does not yet)

The Terraform Registry ingests a published release within minutes and reads the
documentation from the tag's source archive (33 pages are expected today: the
count is taken from docs/ in this checkout). The OpenTofu Registry picks a
release up on its own schedule, an hour or more later.
"""
import json
from pathlib import Path
import sys
import urllib.request

TERRAFORM = "https://registry.terraform.io/v2/"
OPENTOFU = "https://api.opentofu.org/registry/docs/providers/irashack/pocketid/index.json"


def get(url):
    # The OpenTofu API refuses urllib's default User-Agent.
    request = urllib.request.Request(url, headers={"User-Agent": "terraform-provider-pocketid-release-check"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def main(version):
    pages = len(list((Path(__file__).resolve().parent.parent / "docs").rglob("*.md")))
    ok = True
    listing = get(TERRAFORM + "providers/irashack/pocketid?include=provider-versions")
    ids = [row["id"] for row in listing.get("included", []) if row["attributes"]["version"] == version]
    if ids:
        docs = len(get(TERRAFORM + "provider-versions/" + ids[0] + "?include=provider-docs").get("included", []))
        print("Terraform Registry: " + version + " listed, " + str(docs) + " of " + str(pages) + " pages")
        ok = docs == pages
    else:
        print("Terraform Registry: " + version + " not listed yet")
        ok = False
    if "v" + version in [row["id"] for row in get(OPENTOFU)["versions"]]:
        print("OpenTofu Registry: " + version + " listed")
    else:
        print("OpenTofu Registry: " + version + " not listed yet")
        ok = False
    return 0 if ok else 2


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    try:
        sys.exit(main(sys.argv[1]))
    except OSError as error:
        sys.exit("a registry could not be read: " + str(error))
