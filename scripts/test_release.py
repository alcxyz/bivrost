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


if __name__ == "__main__":
    unittest.main()
