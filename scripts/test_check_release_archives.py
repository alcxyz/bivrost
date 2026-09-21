#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import importlib.util
import io
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path


SCRIPT = Path(__file__).with_name("check-release-archives.py")
SPEC = importlib.util.spec_from_file_location("check_release_archives", SCRIPT)
assert SPEC and SPEC.loader
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)


class ArchiveCheckTest(unittest.TestCase):
    version = "1.2.3-SNAPSHOT-abc123"

    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.directory = Path(self.temporary_directory.name)
        self.archives = CHECKER.expected_archives(self.version)
        for name, members in self.archives.items():
            path = self.directory / name
            if name.endswith(".zip"):
                with zipfile.ZipFile(path, "w") as archive:
                    for member in members:
                        archive.writestr(member, member.encode())
            else:
                with tarfile.open(path, "w:gz") as archive:
                    for member in members:
                        data = member.encode()
                        info = tarfile.TarInfo(member)
                        info.size = len(data)
                        archive.addfile(info, io.BytesIO(data))
        self.write_checksums()

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write_checksums(self) -> None:
        lines = []
        for name in sorted(self.archives):
            digest = hashlib.sha256((self.directory / name).read_bytes()).hexdigest()
            lines.append(f"{digest}  {name}\n")
        (self.directory / f"bivrost_{self.version}_checksums.txt").write_text(
            "".join(lines), encoding="utf-8"
        )

    def test_accepts_expected_release_and_infers_snapshot_version(self) -> None:
        self.assertEqual(CHECKER.validate(self.directory, None), self.version)

    def test_rejects_unexpected_archive_target(self) -> None:
        (self.directory / f"bivrost_{self.version}_windows_arm64.zip").touch()
        with self.assertRaisesRegex(CHECKER.ValidationError, "unexpected"):
            CHECKER.validate(self.directory, self.version)

    def test_rejects_checksum_mismatch(self) -> None:
        first_archive = self.directory / next(iter(self.archives))
        first_archive.write_bytes(first_archive.read_bytes() + b"tampered")
        with self.assertRaisesRegex(CHECKER.ValidationError, "checksum does not match"):
            CHECKER.validate(self.directory, self.version)

    def test_rejects_unsafe_archive_member(self) -> None:
        name = f"bivrost_{self.version}_windows_amd64.zip"
        with zipfile.ZipFile(self.directory / name, "w") as archive:
            for member in self.archives[name][1:]:
                archive.writestr(member, member.encode())
            archive.writestr("../bivrost.exe", b"unsafe")
        self.write_checksums()
        with self.assertRaisesRegex(CHECKER.ValidationError, "unsafe member"):
            CHECKER.validate(self.directory, self.version)


if __name__ == "__main__":
    unittest.main()
