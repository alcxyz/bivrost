#!/usr/bin/env python3
"""Tag a reviewed version, upload validated assets without replacing them, then
publish the Homebrew cask and AUR package generated from the same archives."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

REPOSITORY = "alcxyz/bivrost"
TAP_REPOSITORY = "alcxyz/homebrew-tap"
CASK = Path("dist/homebrew/Casks/bivrost.rb")
AUR_URL = "ssh://aur@aur.archlinux.org/bivrost-bin.git"
PKGBUILD = Path("dist/aur/bivrost-bin.pkgbuild")
SRCINFO = Path("dist/aur/bivrost-bin.srcinfo")
# The AUR host keys, matching the fingerprints published on https://aur.archlinux.org/.
AUR_HOST_KEYS = [
    "aur.archlinux.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEuBKrPzbawxA/k2g6NcyV5jmqwJ2s+zpgZGZ7tpLIcN",
    "aur.archlinux.org ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBLMiLrP8pVi5BFX2i3vepSUnpedeiewE5XptnUnau+ZoeUOPkpoCgZZuYfpaIQfhhJJI5qgnjJmr4hyJbe/zxow=",
    "aur.archlinux.org ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDKF9vAFWdgm9Bi8uc+tYRBmXASBb5cB5iZsB7LOWWFeBrLp3r14w0/9S2vozjgqY5sJLDPONWoTTaVTbhe3vwO8CBKZTEt1AcWxuXNlRnk9FliR1/eNB9uz/7y1R0+c1Md+P98AJJSJWKN12nqIDIhjl2S1vOUvm7FNY43fU2knIhEbHybhwWeg+0wxpKwcAd/JeL5i92Uv03MYftOToUijd1pqyVFdJvQFhqD4v3M157jxS5FTOBrccAEjT+zYmFyD8WvKUa9vUclRddNllmBJdy4NyLB8SvVZULUPrP3QOlmzemeKracTlVOUG1wsDbxknF1BwSCU7CmU6UFP90kpWIyz66bP0bl67QAvlIc52Yix7pKJPbw85+zykvnfl2mdROsaT8p8R9nwCdFsBc9IiD0NhPEHcyHRwB8fokXTajk2QnGhL+zP5KnkmXnyQYOCUYo3EKMXIlVOVbPDgRYYT/XqvBuzq5S9rrU70KoI/S5lDnFfx/+lPLdtcnnEPk=",
]
# GoReleaser's default package commit author, as for the other apps' AUR packages.
PACKAGE_AUTHOR = ("goreleaserbot", "bot@goreleaser.com")


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


def newest_release_tag():
    pages = json.loads(command("gh", "api", "--paginate", "--slurp",
                               f"repos/{REPOSITORY}/releases?per_page=100"))
    versions = []
    for page in pages:
        for r in page:
            match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", r["tag_name"])
            if match and not r["draft"] and not r["prerelease"]:
                versions.append(tuple(int(part) for part in match.groups()))
    return "v" + ".".join(map(str, max(versions))) if versions else None


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
        should_release = should_package = False
    else:
        if not exists:
            subprocess.run(["gh", "api", "--method", "POST", f"repos/{REPOSITORY}/git/refs",
                            "-f", f"ref=refs/tags/{tag}", "-f", f"sha={head}"],
                           stdout=subprocess.DEVNULL, check=True)
            subprocess.run(["git", "fetch", "origin", f"refs/tags/{tag}:refs/tags/{tag}"], check=True)
        verify_remote_tag(tag, head)
        release = existing_release(tag)
        should_release = release is None or release["draft"]
        should_package = True
        if not should_release:
            print(f"{tag} is already published; leaving its assets unchanged.")
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"should_release={str(should_release).lower()}\n")
        output.write(f"should_package={str(should_package).lower()}\n")


def manifest_digests(text):
    expected = {}
    for line in text.splitlines():
        digest, name = line.split()
        name = name.removeprefix("*")
        if Path(name).name != name or name in expected:
            raise ValueError("invalid checksum asset name")
        expected[name] = digest
    return expected


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
    expected = manifest_digests(manifest.read_text())
    expected[manifest.name] = hashlib.sha256(manifest.read_bytes()).hexdigest()
    release = existing_release(tag)
    if release is None:
        # Use the creation response: a subsequent list can briefly omit the draft.
        release = json.loads(subprocess.check_output(
            ["gh", "api", "--method", "POST", f"repos/{REPOSITORY}/releases", "--input", "-"],
            input=json.dumps({"tag_name": tag, "target_commitish": head, "name": tag,
                              "draft": True, "generate_release_notes": True}),
            text=True))
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


def check_package(label, text, version_line, digests):
    """Require the release version and only archive digests from the manifest."""
    if version_line not in text.splitlines():
        raise ValueError(f"{label} does not describe this version")
    found = set(re.findall(r"\b[0-9a-f]{64}\b", text))
    if not found or not found <= set(digests.values()):
        raise ValueError(f"{label} does not match the release checksums")


def publish_cask(cask, tag):
    # The tap token comes from the release tap GitHub App and only writes the tap.
    env = dict(os.environ, GH_TOKEN=os.environ["TAP_GITHUB_TOKEN"])
    path = f"repos/{TAP_REPOSITORY}/contents/Casks/{CASK.name}"
    current = subprocess.run(["gh", "api", path], env=env, capture_output=True, text=True)
    body = {"message": f"Brew cask update for bivrost version {tag}",
            "content": base64.b64encode(cask.encode()).decode()}
    if current.returncode == 0:
        existing = json.loads(current.stdout)
        if base64.b64decode(existing["content"]).decode() == cask:
            print("Homebrew cask is already current.")
            return
        body["sha"] = existing["sha"]
    elif "HTTP 404" not in current.stderr:
        raise ValueError("could not read the Homebrew cask from the tap")
    subprocess.run(["gh", "api", "--method", "PUT", path, "--input", "-"], env=env,
                   input=json.dumps(body), text=True, stdout=subprocess.DEVNULL, check=True)


def publish_aur(pkgbuild, srcinfo, tag):
    with tempfile.TemporaryDirectory() as tmp:
        key = Path(tmp) / "aur_key"
        with os.fdopen(os.open(key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as handle:
            handle.write(os.environ["AUR_KEY"].strip() + "\n")
        known_hosts = Path(tmp) / "known_hosts"
        known_hosts.write_text("".join(line + "\n" for line in AUR_HOST_KEYS))
        ssh = (f"ssh -i {key} -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes "
               f"-o UserKnownHostsFile={known_hosts} -F /dev/null")
        env = dict(os.environ, GIT_SSH_COMMAND=ssh)
        checkout = Path(tmp) / "aur"
        subprocess.run(["git", "clone", "--quiet", AUR_URL, str(checkout)], env=env, check=True)
        (checkout / "PKGBUILD").write_text(pkgbuild)
        (checkout / ".SRCINFO").write_text(srcinfo)
        git = ["git", "-C", str(checkout), "-c", f"user.name={PACKAGE_AUTHOR[0]}",
               "-c", f"user.email={PACKAGE_AUTHOR[1]}"]
        subprocess.run(git + ["add", "PKGBUILD", ".SRCINFO"], check=True)
        if subprocess.run(git + ["diff", "--cached", "--quiet"]).returncode == 0:
            print("AUR package is already current.")
            return
        subprocess.run(git + ["commit", "--quiet", "-m", f"Update to {tag}"], check=True)
        subprocess.run(git + ["push", "--quiet", "origin", "HEAD:master"], env=env, check=True)


def packages():
    v = version()
    tag = "v" + v
    head = command("git", "rev-parse", "HEAD")
    if head != os.environ["GITHUB_SHA"] or command("git", "rev-parse", f"{tag}^{{commit}}") != head:
        raise ValueError("release tag does not match the tested checkout")
    verify_remote_tag(tag, head)
    # The release list can briefly lag behind the publish step.
    for attempt in range(6):
        release = existing_release(tag)
        if release is not None and not release["draft"]:
            break
        if attempt < 5:
            time.sleep(5)
    else:
        raise ValueError("release is not published; refusing to publish packages")
    # Rerunning an older release must not roll the packages back.
    newest = newest_release_tag()
    if newest != tag:
        print(f"{newest} is newer than {tag}; leaving the packages unchanged.")
        return
    # A rerun rebuilds the archives, so compare them with what users download.
    manifest = Path("dist") / f"bivrost_{v}_checksums.txt"
    published = [a for a in release["assets"] if a["name"] == manifest.name]
    if len(published) != 1 or subprocess.check_output(
            ["gh", "api", f"repos/{REPOSITORY}/releases/assets/{published[0]['id']}",
             "-H", "Accept: application/octet-stream"]) != manifest.read_bytes():
        raise ValueError("built archives differ from the published release")
    digests = manifest_digests(manifest.read_text())
    cask, pkgbuild, srcinfo = CASK.read_text(), PKGBUILD.read_text(), SRCINFO.read_text()
    check_package("Homebrew cask", cask, f'  version "{v}"', digests)
    check_package("PKGBUILD", pkgbuild, f"pkgver={v}", digests)
    check_package(".SRCINFO", srcinfo, f"\tpkgver = {v}", digests)
    publish_cask(cask, tag)
    publish_aur(pkgbuild, srcinfo, tag)


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["prepare"]:
            prepare()
        elif sys.argv[1:] == ["publish"]:
            publish()
        elif sys.argv[1:] == ["packages"]:
            packages()
        elif sys.argv[1:] == ["check-version"]:
            print(version())
        else:
            raise ValueError("usage: release.py check-version|prepare|publish|packages")
    except (ValueError, subprocess.CalledProcessError, OSError) as error:
        sys.exit(str(error))
