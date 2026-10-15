#!/usr/bin/env python3
"""Validate and publish one canonical fixture candidate."""

import argparse
import ctypes
import errno
import fcntl
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import uuid
from pathlib import Path


MARKER_FORMAT = "goldenrec-fixtures-owner-v1"
MARKER_NAME = ".goldenrec-fixtures-owner"
STABLE_MARKER_ROOT = "/work/tools/goldenrec/fixtures/out"
RUN_NAME = re.compile(r"\.fixture-generation\.[A-Za-z0-9]+")
CANONICAL_FFMPEG = Path("/usr/bin/ffmpeg")
CANONICAL_FFMPEG_VERSION = "7:5.1.9-0+deb12u1"
CANONICAL_IMAGE_ATTESTATION = Path("/usr/local/share/jellyport/fixture-generator-v1")
CANONICAL_IMAGE_ATTESTATION_BYTES = (
    b"format=jellyport-fixture-generator-v1\n"
    b"base=golang:1.25.9-bookworm@sha256:"
    b"298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3\n"
    b"snapshot=20260728T000000Z\n"
    b"ffmpeg=7:5.1.9-0+deb12u1\n"
    b"platform=linux/amd64\n"
)
CANONICAL_APT_SOURCES = Path("/etc/apt/sources.list")
CANONICAL_APT_SOURCES_BYTES = (
    b"deb [check-valid-until=no] "
    b"https://snapshot.debian.org/archive/debian/20260728T000000Z bookworm main\n"
    b"deb [check-valid-until=no] "
    b"https://snapshot.debian.org/archive/debian/20260728T000000Z bookworm-updates main\n"
    b"deb [check-valid-until=no] "
    b"https://snapshot.debian.org/archive/debian-security/20260728T000000Z "
    b"bookworm-security main\n"
)
CANONICAL_OS_RELEASE = Path("/usr/lib/os-release")
AT_FDCWD = -100
RENAME_EXCHANGE = 2


def fail(message):
    raise SystemExit(f"fixture-publisher: {message}")


def marker_bytes(root, uid):
    return (
        json.dumps(
            {"format": MARKER_FORMAT, "root": root, "uid": uid},
            separators=(",", ":"),
            sort_keys=True,
        )
        + "\n"
    ).encode()


def require_directory(path, uid, label):
    try:
        metadata = path.lstat()
    except OSError as error:
        fail(f"cannot inspect {label} {path}: {error}")
    if not stat.S_ISDIR(metadata.st_mode) or path.is_symlink():
        fail(f"{label} {path} is not a plain directory")
    found_mode = stat.S_IMODE(metadata.st_mode)
    if metadata.st_uid != uid or found_mode != 0o700:
        fail(
            f"{label} {path} has mode {found_mode:04o} (want 0700), "
            f"owner {metadata.st_uid} (want {uid})"
        )


def read_private_file(path, uid, label):
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open {label}: {error}")
    with os.fdopen(descriptor, "rb") as file:
        metadata = os.fstat(file.fileno())
        if not stat.S_ISREG(metadata.st_mode):
            fail(f"{label} is not a regular file")
        if metadata.st_uid != uid or stat.S_IMODE(metadata.st_mode) != 0o600:
            fail(f"{label} has unsafe ownership or mode")
        return file.read()


def read_regular_file(path, label):
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open {label}: {error}")
    with os.fdopen(descriptor, "rb") as file:
        if not stat.S_ISREG(os.fstat(file.fileno()).st_mode):
            fail(f"{label} is not a regular file")
        return file.read()


def read_root_file(path, label, mode):
    flags = os.O_RDONLY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        fail(f"cannot open {label}: {error}")
    with os.fdopen(descriptor, "rb") as file:
        metadata = os.fstat(file.fileno())
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != 0
            or stat.S_IMODE(metadata.st_mode) != mode
        ):
            fail(f"{label} has unsafe ownership, type, or mode")
        return file.read()


def parse_os_release(contents):
    values = {}
    for line in contents.decode().splitlines():
        key, separator, value = line.partition("=")
        if separator:
            values[key] = value.strip('"')
    return values


def verify_canonical_environment():
    system = os.uname()
    if system.sysname != "Linux" or system.machine != "x86_64":
        fail(
            "canonical publication requires Linux/x86_64, "
            f"found {system.sysname}/{system.machine}"
        )

    image_attestation = read_root_file(
        CANONICAL_IMAGE_ATTESTATION,
        "canonical image attestation",
        0o644,
    )
    if image_attestation != CANONICAL_IMAGE_ATTESTATION_BYTES:
        fail("canonical image attestation does not match the pinned image")

    os_release = parse_os_release(
        read_root_file(CANONICAL_OS_RELEASE, "Debian release metadata", 0o644)
    )
    if os_release.get("ID") != "debian" or os_release.get("VERSION_ID") != "12":
        fail("canonical publication requires Debian 12")

    apt_sources = read_root_file(
        CANONICAL_APT_SOURCES,
        "canonical Debian snapshot sources",
        0o644,
    )
    if apt_sources != CANONICAL_APT_SOURCES_BYTES:
        fail("Debian package sources do not match the pinned snapshot")

    try:
        query = subprocess.run(
            [
                "/usr/bin/dpkg-query",
                "-W",
                "-f=${Version}",
                "ffmpeg",
            ],
            check=False,
            capture_output=True,
            text=True,
            env={
                "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8",
                "PATH": "/usr/bin:/bin",
            },
        )
    except OSError as error:
        fail(f"cannot query the canonical ffmpeg package: {error}")
    if query.returncode != 0:
        fail(f"cannot query the canonical ffmpeg package: {query.stderr.strip()}")
    if query.stdout != CANONICAL_FFMPEG_VERSION:
        fail(
            f"ffmpeg package is {query.stdout}, "
            f"want {CANONICAL_FFMPEG_VERSION}"
        )

    try:
        ffmpeg_metadata = CANONICAL_FFMPEG.lstat()
    except OSError as error:
        fail(f"cannot inspect canonical ffmpeg: {error}")
    if (
        not stat.S_ISREG(ffmpeg_metadata.st_mode)
        or ffmpeg_metadata.st_uid != 0
        or stat.S_IMODE(ffmpeg_metadata.st_mode) != 0o755
    ):
        fail("canonical ffmpeg has unsafe ownership, type, or mode")


def parse_manifest(contents, label):
    entries = {}
    for line in contents.splitlines():
        digest, separator, encoded_path = line.partition(b"  ")
        try:
            relative = encoded_path.decode()
        except UnicodeDecodeError:
            fail(f"{label} contains a non-UTF-8 path")
        path = Path(relative)
        if (
            not separator
            or len(digest) != 64
            or any(byte not in b"0123456789abcdef" for byte in digest)
            or path.is_absolute()
            or not path.parts
            or ".." in path.parts
            or relative in entries
        ):
            fail(f"{label} contains an invalid entry")
        entries[relative] = digest.decode()
    if len(entries) != 58:
        fail(f"{label} has {len(entries)} entries, want 58")
    media = sum(path.endswith((".mkv", ".mp3")) for path in entries)
    if media != 30:
        fail(f"{label} has {media} media entries, want 30")
    if "FFMPEG.version" not in entries:
        fail(f"{label} does not attest FFMPEG.version")
    return entries


def actual_manifest(root):
    result = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            fail(f"candidate contains a symlink: {path.relative_to(root)}")
        if not path.is_file() or path.name in {MARKER_NAME, "MANIFEST.sha256"}:
            continue
        relative = path.relative_to(root).as_posix()
        result[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def atomic_write(path, contents, mode):
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(temporary, flags, mode)
    try:
        with os.fdopen(descriptor, "wb") as file:
            os.fchmod(file.fileno(), mode)
            file.write(contents)
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def validate_existing_output(output, uid):
    if not output.exists() and not output.is_symlink():
        return
    require_directory(output, uid, "accepted output")
    actual = read_private_file(output / MARKER_NAME, uid, "accepted output marker")
    if actual != marker_bytes(STABLE_MARKER_ROOT, uid):
        fail("accepted output marker does not authorize replacement")


def exchange_directories(first, second):
    renameat2 = getattr(ctypes.CDLL(None, use_errno=True), "renameat2", None)
    if renameat2 is None:
        return False
    result = renameat2(
        AT_FDCWD,
        os.fsencode(first),
        AT_FDCWD,
        os.fsencode(second),
        RENAME_EXCHANGE,
    )
    if result == 0:
        return True
    error = ctypes.get_errno()
    if error in {errno.ENOSYS, errno.EINVAL, errno.EOPNOTSUPP, errno.EXDEV}:
        return False
    raise OSError(error, os.strerror(error))



def remove_installed_candidate(output, uid):
    if not output.exists() or output.is_symlink():
        return
    metadata = output.lstat()
    if not stat.S_ISDIR(metadata.st_mode):
        return
    if metadata.st_uid != uid or stat.S_IMODE(metadata.st_mode) != 0o700:
        return
    marker_path = output / MARKER_NAME
    try:
        actual = read_private_file(marker_path, uid, "installed candidate marker")
    except SystemExit:
        return
    if actual != marker_bytes(STABLE_MARKER_ROOT, uid):
        return
    shutil.rmtree(output)


def publish(candidate, output, source_manifest, manifest, attest, uid):
    lock = output.parent / ".fixture-publish.lock"
    lock_flags = os.O_RDWR
    if hasattr(os, "O_NOFOLLOW"):
        lock_flags |= os.O_NOFOLLOW
    try:
        lock_descriptor = os.open(lock, lock_flags)
    except OSError as error:
        fail(f"cannot open publication lock {lock}: {error}")
    lock_metadata = os.fstat(lock_descriptor)
    if not stat.S_ISREG(lock_metadata.st_mode) or lock_metadata.st_uid != uid:
        os.close(lock_descriptor)
        fail(f"publication lock has unsafe ownership or type: {lock}")

    backup = output.parent / f".out.previous.{uuid.uuid4().hex}"
    installed = False
    moved_old = False
    exchanged = False
    committed = False
    had_prior_output = output.exists() or output.is_symlink()
    try:
        fcntl.flock(lock_descriptor, fcntl.LOCK_EX)
        if not attest and read_regular_file(source_manifest, "source manifest") != manifest:
            fail("candidate differs from source-manifest.sha256")
        validate_existing_output(output, uid)
        atomic_write(candidate / MARKER_NAME, marker_bytes(STABLE_MARKER_ROOT, uid), 0o600)

        if output.exists() or output.is_symlink():
            exchanged = exchange_directories(candidate, output)
            if exchanged:
                installed = True
            else:
                os.rename(output, backup)
                moved_old = True
        if not exchanged:
            try:
                os.rename(candidate, output)
                installed = True
            except BaseException:
                if moved_old:
                    os.rename(backup, output)
                    moved_old = False
                raise

        if attest:
            atomic_write(source_manifest, manifest, 0o644)
        committed = True

        if exchanged:
            shutil.rmtree(candidate)
            exchanged = False
        elif moved_old:
            try:
                shutil.rmtree(backup)
                moved_old = False
            except OSError as error:
                print(f"fixture-publisher: warning: cannot remove prior output {backup}: {error}", file=sys.stderr)
    except BaseException:
        if committed:
            raise
        if exchanged:
            try:
                if exchange_directories(candidate, output):
                    installed = False
                    exchanged = False
            except OSError:
                pass
        elif installed:
            if not had_prior_output:
                remove_installed_candidate(output, uid)
                installed = False
            else:
                try:
                    os.rename(output, candidate)
                    installed = False
                except OSError:
                    pass
        if moved_old and not output.exists():
            try:
                os.rename(backup, output)
                moved_old = False
            except OSError:
                pass
        raise
    finally:
        fcntl.flock(lock_descriptor, fcntl.LOCK_UN)
        os.close(lock_descriptor)


def generate_candidate(repository, run_root, canonical_ffmpeg):
    candidate = run_root / "out"
    generator = repository / "tools" / "goldenrec" / "fixtures" / "gen.sh"
    environment = {
        "FFMPEG": str(canonical_ffmpeg),
        "GOLDENREC_FIXTURES_DIR": str(candidate),
        "HOME": "/tmp",
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "TZ": "UTC",
    }
    try:
        result = subprocess.run(
            ["/bin/bash", str(generator)],
            cwd=repository,
            check=False,
            env=environment,
        )
    except OSError as error:
        fail(f"cannot start the canonical generator: {error}")
    if result.returncode != 0:
        raise SystemExit(result.returncode)
    return candidate


def validate_candidate(candidate, uid):
    require_directory(candidate, uid, "candidate output")
    candidate_marker = read_private_file(
        candidate / MARKER_NAME,
        uid,
        "candidate marker",
    )
    if candidate_marker != marker_bytes(str(candidate), uid):
        fail("candidate marker does not bind the candidate path")

    manifest = read_regular_file(candidate / "MANIFEST.sha256", "candidate manifest")
    entries = parse_manifest(manifest, "candidate manifest")
    if actual_manifest(candidate) != entries:
        fail("candidate files do not match its complete manifest")
    return manifest


def main(
    argv=None,
    *,
    verify_environment=verify_canonical_environment,
    canonical_ffmpeg=CANONICAL_FFMPEG,
):
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", type=Path, default=Path("/work"))
    parser.add_argument("--candidate-name", required=True)
    parser.add_argument("--attest", action="store_true")
    arguments = parser.parse_args(argv)

    if not RUN_NAME.fullmatch(arguments.candidate_name):
        fail("candidate name is invalid")

    verify_environment()

    repository = arguments.repository.resolve()
    fixtures = repository / "tools" / "goldenrec" / "fixtures"
    run_root = fixtures / arguments.candidate_name
    output = fixtures / "out"
    source_manifest = fixtures / "source-manifest.sha256"
    uid = os.getuid()

    require_directory(run_root, uid, "private generation directory")
    try:
        if any(run_root.iterdir()):
            fail("private generation directory is not empty")
    except OSError as error:
        fail(f"cannot inspect private generation directory: {error}")

    validate_existing_output(output, uid)
    try:
        candidate = generate_candidate(repository, run_root, canonical_ffmpeg)
        manifest = validate_candidate(candidate, uid)
        publish(
            candidate,
            output,
            source_manifest,
            manifest,
            arguments.attest,
            uid,
        )
    finally:
        try:
            shutil.rmtree(run_root)
        except FileNotFoundError:
            pass
        except OSError as error:
            print(
                f"fixture-publisher: warning: cannot remove {run_root}: {error}",
                file=sys.stderr,
            )

    print(f"published canonical fixture projection at {output}")
    if arguments.attest:
        print(f"attested canonical fixture manifest at {source_manifest}")


if __name__ == "__main__":
    main()
