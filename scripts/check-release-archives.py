#!/usr/bin/env python3
"""Validate the complete set of Bivrost release archives and checksums."""

from __future__ import annotations

import argparse
import hashlib
import stat
import sys
import tarfile
import zipfile
from collections import Counter
from pathlib import Path, PurePosixPath


ARCHIVE_SUFFIXES = (
    "_linux_amd64.tar.gz",
    "_linux_arm64.tar.gz",
    "_darwin_amd64.tar.gz",
    "_darwin_arm64.tar.gz",
    "_windows_amd64.zip",
)
COMMON_MEMBERS = ("README.md", "LICENSE", "config.example.json")


class ValidationError(Exception):
    pass


def expected_archives(version: str) -> dict[str, tuple[str, ...]]:
    prefix = f"bivrost_{version}"
    return {
        f"{prefix}{suffix}": (
            ("bivrost.exe",) if "_windows_" in suffix else ("bivrost",)
        )
        + COMMON_MEMBERS
        for suffix in ARCHIVE_SUFFIXES
    }


def infer_version(directory: Path) -> str:
    versions: set[str] = set()
    for path in directory.iterdir():
        if not path.is_file() or not path.name.startswith("bivrost_"):
            continue
        for suffix in ARCHIVE_SUFFIXES:
            if path.name.endswith(suffix):
                versions.add(path.name[len("bivrost_") : -len(suffix)])
                break

    if not versions:
        raise ValidationError("could not infer a version from release archive names")
    if len(versions) != 1:
        raise ValidationError(
            "release archives contain multiple versions: " + ", ".join(sorted(versions))
        )
    return versions.pop()


def validate_version(version: str) -> None:
    if not version or version in {".", ".."} or "/" in version or "\\" in version:
        raise ValidationError(f"invalid version for an artifact filename: {version!r}")


def unsafe_member_reason(name: str) -> str | None:
    if not name or "\x00" in name:
        return "empty or NUL-containing path"
    if "\\" in name:
        return "backslash path separator"
    path = PurePosixPath(name)
    if path.is_absolute() or name.startswith("/"):
        return "absolute path"
    if any(part in {"", ".", ".."} for part in path.parts):
        return "relative traversal component"
    if path.parts and len(path.parts[0]) >= 2 and path.parts[0][1] == ":":
        return "drive-qualified path"
    return None


def validate_member_names(
    archive_name: str, names: list[str], expected: tuple[str, ...]
) -> None:
    for name in names:
        if reason := unsafe_member_reason(name):
            raise ValidationError(f"{archive_name}: unsafe member {name!r}: {reason}")
    if Counter(names) != Counter(expected):
        raise ValidationError(
            f"{archive_name}: members are {sorted(names)!r}, expected {sorted(expected)!r}"
        )


def validate_tar(path: Path, expected: tuple[str, ...]) -> None:
    try:
        with tarfile.open(path, mode="r:gz") as archive:
            members = archive.getmembers()
            validate_member_names(path.name, [member.name for member in members], expected)
            for member in members:
                if not member.isfile():
                    raise ValidationError(
                        f"{path.name}: member {member.name!r} is not a regular file"
                    )
    except (tarfile.TarError, OSError) as error:
        raise ValidationError(f"{path.name}: cannot read tar archive: {error}") from error


def validate_zip(path: Path, expected: tuple[str, ...]) -> None:
    try:
        with zipfile.ZipFile(path) as archive:
            members = archive.infolist()
            validate_member_names(path.name, [member.filename for member in members], expected)
            for member in members:
                mode = member.external_attr >> 16
                if member.is_dir() or stat.S_ISLNK(mode):
                    raise ValidationError(
                        f"{path.name}: member {member.filename!r} is not a regular file"
                    )
                with archive.open(member) as contents:
                    while contents.read(1024 * 1024):
                        pass
    except (zipfile.BadZipFile, OSError) as error:
        raise ValidationError(f"{path.name}: cannot read zip archive: {error}") from error


def read_checksums(path: Path) -> dict[str, str]:
    checksums: dict[str, str] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as error:
        raise ValidationError(f"cannot read {path.name}: {error}") from error

    for line_number, line in enumerate(lines, 1):
        parts = line.split(maxsplit=1)
        if len(parts) != 2:
            raise ValidationError(f"{path.name}:{line_number}: malformed checksum line")
        digest, name = parts
        name = name.removeprefix("*")
        if len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest):
            raise ValidationError(f"{path.name}:{line_number}: invalid SHA-256 digest")
        if unsafe_member_reason(name) or PurePosixPath(name).name != name:
            raise ValidationError(f"{path.name}:{line_number}: unsafe artifact name {name!r}")
        if name in checksums:
            raise ValidationError(f"{path.name}:{line_number}: duplicate artifact {name!r}")
        checksums[name] = digest
    return checksums


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as contents:
        while chunk := contents.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def validate(directory: Path, version: str | None) -> str:
    if not directory.is_dir():
        raise ValidationError(f"archive directory does not exist: {directory}")
    version = version or infer_version(directory)
    validate_version(version)

    archives = expected_archives(version)
    checksum_name = f"bivrost_{version}_checksums.txt"
    expected_release_files = set(archives) | {checksum_name}
    actual_release_files = {
        path.name
        for path in directory.iterdir()
        if path.is_file()
        and path.name.startswith("bivrost_")
        and (
            path.name.endswith(".tar.gz")
            or path.name.endswith(".zip")
            or path.name.endswith("_checksums.txt")
        )
    }
    if actual_release_files != expected_release_files:
        missing = sorted(expected_release_files - actual_release_files)
        unexpected = sorted(actual_release_files - expected_release_files)
        details = []
        if missing:
            details.append("missing " + ", ".join(missing))
        if unexpected:
            details.append("unexpected " + ", ".join(unexpected))
        raise ValidationError("release file set mismatch: " + "; ".join(details))

    for name, members in archives.items():
        path = directory / name
        if path.is_symlink():
            raise ValidationError(f"{name}: archive must not be a symbolic link")
        if name.endswith(".zip"):
            validate_zip(path, members)
        else:
            validate_tar(path, members)

    checksum_path = directory / checksum_name
    if checksum_path.is_symlink():
        raise ValidationError(f"{checksum_name}: checksum file must not be a symbolic link")
    checksums = read_checksums(checksum_path)
    if set(checksums) != set(archives):
        raise ValidationError(
            f"{checksum_name}: covers {sorted(checksums)!r}, expected {sorted(archives)!r}"
        )
    for name, expected_digest in checksums.items():
        actual_digest = sha256(directory / name)
        if actual_digest != expected_digest:
            raise ValidationError(f"{name}: SHA-256 checksum does not match")
    return version


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "directory", nargs="?", type=Path, default=Path("dist"), help="GoReleaser output directory"
    )
    parser.add_argument("--version", help="expected artifact version; inferred when omitted")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        version = validate(args.directory, args.version)
    except ValidationError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    print(f"validated 5 Bivrost release archives for {version}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
