import base64
import json
import subprocess
import unittest
from unittest.mock import patch
import release


class AssetPublicationTests(unittest.TestCase):
    def test_remote_tag_must_resolve_to_tested_commit(self):
        with patch("release.api", side_effect=[
            {"object": {"type": "tag", "sha": "annotation"}},
            {"object": {"type": "commit", "sha": "tested"}},
        ]):
            release.verify_remote_tag("v0.1.0", "tested")
        with patch("release.api", return_value={"object": {"type": "commit", "sha": "moved"}}):
            with self.assertRaisesRegex(ValueError, "remote release tag"):
                release.verify_remote_tag("v0.1.0", "tested")

    def test_paginated_assets_include_later_pages(self):
        with patch("release.command", return_value='[[{"name":"one"}],[{"name":"unexpected"}]]'):
            assets = release.paginated_api("releases/1/assets?per_page=100")
        with self.assertRaisesRegex(ValueError, "unexpected"):
            release.assets_to_upload({"one": "hash"}, assets, lambda _: "hash")

    def test_new_draft_uses_creation_response_without_relisting(self):
        with (
            patch("release.version", return_value="0.1.0"),
            patch("release.command", return_value="tested"),
            patch.dict("release.os.environ", {"GITHUB_SHA": "tested"}),
            patch("release.verify_remote_tag"),
            patch("release.subprocess.run"),
            patch("release.existing_release", return_value=None) as lookup,
            patch("release.subprocess.check_output", return_value='{"id":42,"draft":true}'),
            patch("release.paginated_api", return_value=[]) as assets,
            patch("release.assets_to_upload", return_value=[]),
            patch("pathlib.Path.read_text", return_value="hash archive.tar.gz"),
            patch("pathlib.Path.read_bytes", return_value=b"manifest"),
        ):
            release.publish()
        lookup.assert_called_once_with("v0.1.0")
        self.assertEqual(assets.call_args_list[0].args, ("releases/42/assets?per_page=100",))

    def test_retry_uploads_only_missing_assets(self):
        self.assertEqual(release.assets_to_upload(
            {"one": "hash1", "two": "hash2"}, [{"name": "one"}],
            lambda asset: "hash1"), ["two"])

    def test_mismatch_never_replaces_an_asset(self):
        with self.assertRaisesRegex(ValueError, "existing asset differs"):
            release.assets_to_upload({"one": "new"}, [{"name": "one"}], lambda _: "old")

    def test_unexpected_or_duplicate_assets_block_publication(self):
        for assets in [[{"name": "unexpected"}], [{"name": "one"}, {"name": "one"}]]:
            with self.assertRaises(ValueError):
                release.assets_to_upload({"one": "hash"}, assets, lambda _: "hash")

    def test_version_rejects_shell_content_and_noncanonical_semver(self):
        for value in ["1.0.0;echo unsafe", "v1.0.0", "01.0.0", "1.0", "1.0.0-rc1"]:
            with patch("pathlib.Path.read_text", return_value=value):
                with self.assertRaises(ValueError):
                    release.version()



DIGEST = "a" * 64
OTHER = "b" * 64


class PackagePublicationTests(unittest.TestCase):
    def test_package_must_name_the_version_and_only_manifest_digests(self):
        digests = {"bivrost_0.4.0_linux_amd64.tar.gz": DIGEST}
        release.check_package("cask", f'  version "0.4.0"\n  sha256 "{DIGEST}"', '  version "0.4.0"', digests)
        for text in [f'  version "0.4.1"\n  sha256 "{DIGEST}"',
                     f'  version "0.4.0"\n  sha256 "{OTHER}"',
                     '  version "0.4.0"\n']:
            with self.assertRaises(ValueError):
                release.check_package("cask", text, '  version "0.4.0"', digests)

    def test_packages_refuse_an_unpublished_release(self):
        for found in [None, {"draft": True, "assets": []}]:
            with (
                patch("release.version", return_value="0.4.0"),
                patch("release.command", return_value="tested"),
                patch.dict("release.os.environ", {"GITHUB_SHA": "tested"}),
                patch("release.verify_remote_tag"),
                patch("release.existing_release", return_value=found),
                patch("release.time.sleep"),
                patch("release.publish_cask") as cask,
            ):
                with self.assertRaisesRegex(ValueError, "not published"):
                    release.packages()
            cask.assert_not_called()

    def test_packages_refuse_archives_that_differ_from_the_release(self):
        published = {"draft": False, "assets": [{"name": "bivrost_0.4.0_checksums.txt", "id": 7}]}
        with (
            patch("release.version", return_value="0.4.0"),
            patch("release.command", return_value="tested"),
            patch.dict("release.os.environ", {"GITHUB_SHA": "tested"}),
            patch("release.verify_remote_tag"),
            patch("release.existing_release", return_value=published),
            patch("release.newest_release_tag", return_value="v0.4.0"),
            patch("release.subprocess.check_output", return_value=b"published"),
            patch("pathlib.Path.read_bytes", return_value=b"rebuilt"),
            patch("release.publish_cask") as cask,
        ):
            with self.assertRaisesRegex(ValueError, "differ from the published release"):
                release.packages()
        cask.assert_not_called()

    def test_rerunning_an_older_release_leaves_the_packages_alone(self):
        with (
            patch("release.version", return_value="0.4.1"),
            patch("release.command", return_value="tested"),
            patch.dict("release.os.environ", {"GITHUB_SHA": "tested"}),
            patch("release.verify_remote_tag"),
            patch("release.existing_release", return_value={"draft": False, "assets": []}),
            patch("release.newest_release_tag", return_value="v0.4.2"),
            patch("release.subprocess.check_output") as download,
            patch("release.publish_cask") as cask,
            patch("release.publish_aur") as aur,
        ):
            release.packages()
        download.assert_not_called()
        cask.assert_not_called()
        aur.assert_not_called()

    def test_newest_release_ignores_drafts_prereleases_and_other_tags(self):
        pages = [[{"tag_name": "v0.10.0", "draft": True, "prerelease": False},
                  {"tag_name": "v0.9.0", "draft": False, "prerelease": False}],
                 [{"tag_name": "v0.11.0", "draft": False, "prerelease": True},
                  {"tag_name": "v0.4.2", "draft": False, "prerelease": False},
                  {"tag_name": "nightly", "draft": False, "prerelease": False}]]
        with patch("release.command", return_value=json.dumps(pages)):
            self.assertEqual(release.newest_release_tag(), "v0.9.0")

    def test_current_cask_is_left_unchanged(self):
        current = subprocess.CompletedProcess([], 0, json.dumps(
            {"sha": "blob", "content": base64.b64encode(b"cask").decode()}), "")
        with (
            patch.dict("release.os.environ", {"TAP_GITHUB_TOKEN": "tap"}),
            patch("release.subprocess.run", return_value=current) as run,
        ):
            release.publish_cask("cask", "v0.4.0")
        self.assertEqual(run.call_count, 1)

    def test_changed_cask_updates_the_existing_file_with_the_tap_token(self):
        current = subprocess.CompletedProcess([], 0, json.dumps(
            {"sha": "blob", "content": base64.b64encode(b"old").decode()}), "")
        with (
            patch.dict("release.os.environ", {"TAP_GITHUB_TOKEN": "tap", "GH_TOKEN": "repo"}),
            patch("release.subprocess.run", side_effect=[current, None]) as run,
        ):
            release.publish_cask("new", "v0.4.0")
        update = run.call_args_list[1]
        self.assertEqual(json.loads(update.kwargs["input"])["sha"], "blob")
        self.assertEqual(update.kwargs["env"]["GH_TOKEN"], "tap")

    def test_cask_read_failure_is_not_treated_as_missing(self):
        failed = subprocess.CompletedProcess([], 1, "", "gh: Bad credentials (HTTP 401)")
        with (
            patch.dict("release.os.environ", {"TAP_GITHUB_TOKEN": "tap"}),
            patch("release.subprocess.run", return_value=failed),
        ):
            with self.assertRaisesRegex(ValueError, "could not read"):
                release.publish_cask("cask", "v0.4.0")

    def test_aur_host_keys_are_pinned(self):
        types = sorted(line.split()[1] for line in release.AUR_HOST_KEYS)
        self.assertEqual(types, ["ecdsa-sha2-nistp256", "ssh-ed25519", "ssh-rsa"])
        self.assertTrue(all(line.startswith("aur.archlinux.org ") for line in release.AUR_HOST_KEYS))


if __name__ == "__main__":
    unittest.main()
