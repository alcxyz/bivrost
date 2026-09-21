#!/usr/bin/env python3
"""Tag a reviewed version, then upload validated assets without replacing them."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

REPOSITORY = "alcxyz/bivrost"


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def api(path):
    return json.loads(command("gh", "api", f"repos/{REPOSITORY}/{path}"))


def paginated_api(path):
    pages = json.loads(command("gh", "api", "--paginate", "--slurp",
                               f"repos/{REPOSITORY}/{path}"))
    return [item for page in pages for item in page]


def verify_remote_tag(tag, head):
    obj = api(f"git/ref/tags/{tag}")["object"]
    for _ in range(10):
        if obj["type"] != "tag":
            break
        obj = api(f"git/tags/{obj['sha']}")["object"]
    if obj["type"] != "commit" or obj["sha"] != head:
        raise ValueError("remote release tag does not match the tested checkout")


def version():
    value = Path("VERSION").read_text().strip()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", value):
        raise ValueError("VERSION must contain plain X.Y.Z semver")
    return value


def existing_release(tag):
    # A failed API call is an error, not evidence that a release is absent.
    pages = json.loads(command("gh", "api", "--paginate", "--slurp",
                               f"repos/{REPOSITORY}/releases?per_page=100"))
    return next((r for page in pages for r in page if r["tag_name"] == tag), None)


def prepare():
    tag = "v" + version()
    head = command("git", "rev-parse", "HEAD")
    if head != os.environ["GITHUB_SHA"]:
        raise ValueError("checkout does not match the tested workflow revision")
    tag_status = subprocess.run(["git", "show-ref", "--verify", "--quiet", f"refs/tags/{tag}"]).returncode
    if tag_status not in (0, 1):
        raise ValueError("could not inspect release tag")
    exists = tag_status == 0
    if exists and command("git", "rev-parse", f"{tag}^{{commit}}") != head:
        print(f"{tag} belongs to an earlier commit; bump VERSION to release again.")
        should_release = False
    else:
        if not exists:
            subprocess.run(["gh", "api", "--method", "POST", f"repos/{REPOSITORY}/git/refs",
                            "-f", f"ref=refs/tags/{tag}", "-f", f"sha={head}"],
                           stdout=subprocess.DEVNULL, check=True)
            subprocess.run(["git", "fetch", "origin", f"refs/tags/{tag}:refs/tags/{tag}"], check=True)
        verify_remote_tag(tag, head)
        release = existing_release(tag)
        should_release = release is None or release["draft"]
        if not should_release:
            print(f"{tag} is already published; leaving its assets unchanged.")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"should_release={str(should_release).lower()}\n")


def assets_to_upload(expected, assets, fetch_digest):
    remote = {}
    for asset in assets:
        name = asset["name"]
        if name in remote:
            raise ValueError(f"duplicate remote asset: {name}")
        remote[name] = asset
    if remote.keys() - expected.keys():
        raise ValueError("release contains unexpected assets; refusing to publish")
    for name in remote:
        if fetch_digest(remote[name]) != expected[name]:
            raise ValueError(f"existing asset differs: {name}; publish a new version instead")
    return sorted(expected.keys() - remote.keys())


def publish():
    v = version()
    tag = "v" + v
    head = command("git", "rev-parse", "HEAD")
    if head != os.environ["GITHUB_SHA"] or command("git", "rev-parse", f"{tag}^{{commit}}") != head:
        raise ValueError("release tag does not match the tested checkout")
    verify_remote_tag(tag, head)
    subprocess.run([sys.executable, "scripts/check-release-archives.py"], check=True)
    manifest = Path("dist") / f"bivrost_{v}_checksums.txt"
    expected = {}
    for line in manifest.read_text().splitlines():
        digest, name = line.split()
        name = name.removeprefix("*")
        if Path(name).name != name or name in expected:
            raise ValueError("invalid checksum asset name")
        expected[name] = digest
    expected[manifest.name] = hashlib.sha256(manifest.read_bytes()).hexdigest()
    release = existing_release(tag)
    if release is None:
        subprocess.run(["gh", "release", "create", tag, "--repo", REPOSITORY,
                        "--draft", "--verify-tag", "--target", head, "--title", tag,
                        "--generate-notes"], check=True)
        release = existing_release(tag)
    if not release["draft"]:
        raise ValueError("release is already published; refusing to mutate it")

    def remote_digest(asset):
        data = subprocess.check_output(["gh", "api", f"repos/{REPOSITORY}/releases/assets/{asset['id']}",
                                        "-H", "Accept: application/octet-stream"])
        return hashlib.sha256(data).hexdigest()

    assets = paginated_api(f"releases/{release['id']}/assets?per_page=100")
    for name in assets_to_upload(expected, assets, remote_digest):
        subprocess.run(["gh", "release", "upload", tag, str(Path("dist") / name),
                        "--repo", REPOSITORY], check=True)
    # Re-download every uploaded file before exposing a complete release.
    assets = paginated_api(f"releases/{release['id']}/assets?per_page=100")
    if assets_to_upload(expected, assets, remote_digest):
        raise ValueError("release assets are incomplete")
    verify_remote_tag(tag, head)
    subprocess.run(["gh", "release", "edit", tag, "--repo", REPOSITORY,
                    "--draft=false", "--verify-tag"], check=True)


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["prepare"]:
            prepare()
        elif sys.argv[1:] == ["publish"]:
            publish()
        elif sys.argv[1:] == ["check-version"]:
            print(version())
        else:
            raise ValueError("usage: release.py check-version|prepare|publish")
    except (ValueError, subprocess.CalledProcessError, OSError) as error:
        sys.exit(str(error))
