#!/usr/bin/env python3
"""Store complete SQLite backups in Releases; pin verified assets in Git."""
import argparse
from contextlib import closing
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[2]
ASSETS = ("catalog.db.gz", "catalog.snapshot.sql.gz")


def run(*args):
    subprocess.run(args, check=True, cwd=ROOT)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def connect_readonly(path):
    return sqlite3.connect(Path(path).resolve().as_uri() + "?mode=ro", uri=True)


def check_database(path):
    with closing(connect_readonly(path)) as db:
        if db.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise ValueError("SQLite integrity check failed")
        if db.execute("PRAGMA foreign_key_check").fetchone():
            raise ValueError("SQLite foreign key check failed")


def content_hash(path):
    # Includes every table, including refresh and resume checkpoints. Physical
    # SQLite page layout changes alone must not create another release.
    digest = hashlib.sha256()
    with closing(connect_readonly(path)) as db:
        for line in db.iterdump():
            digest.update((line + "\n").encode())
    return digest.hexdigest()


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2) + "\n")


def prepare(args):
    output = Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        raise ValueError(f"Package directory must be empty: {output}")
    source = Path(args.db)
    if not source.is_file():
        raise ValueError(f"Database does not exist: {source}")
    tag = args.tag or ("catalog-" + datetime.datetime.now(datetime.timezone.utc).
                       strftime("%Y%m%dT%H%M%SZ-") + uuid.uuid4().hex[:8])
    if not re.fullmatch(r"catalog-[A-Za-z0-9._-]+", tag):
        raise ValueError("Release tag must start with catalog- and use letters, digits, ., _, -")
    with tempfile.TemporaryDirectory() as temp:
        backup = Path(temp) / "catalog.db"
        # SQLite backup API includes committed WAL data and all tables.
        with closing(connect_readonly(source)) as src, closing(sqlite3.connect(backup)) as dst:
            src.backup(dst)
            dst.execute("PRAGMA journal_mode=DELETE")
        snapshot = output.resolve() / ASSETS[1]
        run("go", "run", "./cmd/spotwufamily", "db", "snapshot",
            "--db", str(backup), "--snapshot", str(snapshot))
        run("go", "run", "./cmd/spotwufamily", "db", "verify",
            "--db", str(backup), "--snapshot", str(snapshot))
        with backup.open("rb") as src, (output / ASSETS[0]).open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as dst:
                shutil.copyfileobj(src, dst)
        manifest = {
            "version": 1,
            "repository": args.repo,
            "tag": tag,
            "content_sha256": content_hash(backup),
            "assets": {name: {"sha256": sha256(output / name),
                              "size": (output / name).stat().st_size} for name in ASSETS},
        }
    write_json(output / "catalog.release.json", manifest)
    print(f"Prepared {tag} in {output}")


def read_manifest(path, repo):
    manifest = json.loads(Path(path).read_text())
    if manifest.get("version") != 1 or manifest.get("repository") != repo:
        raise ValueError("Unsupported manifest version or unexpected repository")
    if not re.fullmatch(r"catalog-[A-Za-z0-9._-]+", manifest.get("tag", "")):
        raise ValueError("Invalid catalog release tag")
    if set(manifest.get("assets", {})) != set(ASSETS):
        raise ValueError("Unexpected release assets")
    for item in manifest["assets"].values():
        if not re.fullmatch(r"[a-f0-9]{64}", item.get("sha256", "")):
            raise ValueError("Invalid asset checksum")
        if not isinstance(item.get("size"), int) or not 0 < item["size"] < 2 * 1024**3:
            raise ValueError("Invalid asset size")
    if not re.fullmatch(r"[a-f0-9]{64}", manifest.get("content_sha256", "")):
        raise ValueError("Invalid catalog content checksum")
    return manifest


def check_assets(directory, manifest):
    for name, item in manifest["assets"].items():
        path = Path(directory) / name
        if path.stat().st_size != item["size"] or sha256(path) != item["sha256"]:
            raise ValueError(f"Asset checksum/size mismatch: {name}")


def publish(args):
    package = Path(args.output)
    manifest = read_manifest(package / "catalog.release.json", args.repo)
    check_assets(package, manifest)
    pointer = Path(args.manifest)
    if pointer.exists() and read_manifest(pointer, args.repo)["content_sha256"] == manifest["content_sha256"]:
        print("No catalog content changes; keeping current release.")
        return
    tag = manifest["tag"]
    # Never replace existing assets. --latest=false keeps software releases latest.
    run("gh", "release", "create", tag, "--repo", args.repo,
        "--target", args.target, "--latest=false", "--title", f"Catalog backup {tag}",
        "--notes", "Complete SQLite backup, logical snapshot and SHA-256 manifest. "
        "The version used by the site is pinned in data/catalog.release.json.",
        *(str(package / name) for name in (*ASSETS, "catalog.release.json")))
    # Update Git's pointer only after all assets have been successfully uploaded.
    pointer.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(package / "catalog.release.json", pointer)
    print(f"Published https://github.com/{args.repo}/releases/tag/{tag}")


def install(directory, manifest, db_path, snapshot_path, force=False):
    db_path, snapshot_path = Path(db_path), Path(snapshot_path)
    if db_path.resolve() == snapshot_path.resolve():
        raise ValueError("Database and snapshot paths must differ")
    for suffix in ("-wal", "-shm", "-journal"):
        if Path(str(db_path) + suffix).exists():
            raise ValueError("Close SQLite clients and remove sidecars before fetching")
    if not force and (db_path.exists() or snapshot_path.exists()):
        raise ValueError("Local catalog exists; use --force explicitly to replace it")
    check_assets(directory, manifest)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    snapshot_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=db_path.parent) as temp:
        backup = Path(temp) / "catalog.db"
        with gzip.open(Path(directory) / ASSETS[0], "rb") as src, backup.open("wb") as dst:
            shutil.copyfileobj(src, dst)
        check_database(backup)
        if content_hash(backup) != manifest["content_sha256"]:
            raise ValueError("Catalog content checksum mismatch")
        # Validate the gzip stream before replacing either local file.
        with gzip.open(Path(directory) / ASSETS[1], "rb") as stream:
            for _ in iter(lambda: stream.read(1024 * 1024), b""):
                pass
        with tempfile.NamedTemporaryFile(dir=snapshot_path.parent, delete=False) as staged:
            stage_path = Path(staged.name)
            with (Path(directory) / ASSETS[1]).open("rb") as src:
                shutil.copyfileobj(src, staged)
        try:
            os.replace(backup, db_path)
            os.replace(stage_path, snapshot_path)
        finally:
            stage_path.unlink(missing_ok=True)


def fetch(args):
    manifest = read_manifest(args.manifest, args.repo)
    if not args.force and (Path(args.db).exists() or Path(args.snapshot).exists()):
        raise ValueError("Local catalog exists; use --force explicitly to replace it")
    with tempfile.TemporaryDirectory() as temp:
        run("gh", "release", "download", manifest["tag"], "--repo", args.repo,
            "--dir", temp, "--pattern", ASSETS[0], "--pattern", ASSETS[1])
        install(temp, manifest, args.db, args.snapshot, args.force)
    print(f"Restored {manifest['tag']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("prepare", "publish", "fetch"))
    parser.add_argument("--repo", default="javiyt/spotwufamily")
    parser.add_argument("--db", default="data/catalog.db")
    parser.add_argument("--snapshot", default="data/catalog.snapshot.sql.gz")
    parser.add_argument("--manifest", default="data/catalog.release.json")
    parser.add_argument("--output", default="build/catalog-release")
    parser.add_argument("--tag")
    parser.add_argument("--target", default="main", help="Remote commit/ref for a new release tag")
    parser.add_argument("--force", action="store_true", help="Replace existing local catalog on fetch")
    args = parser.parse_args()
    os.chdir(ROOT)
    try:
        {"prepare": prepare, "publish": publish, "fetch": fetch}[args.command](args)
    except (ValueError, OSError, sqlite3.Error, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Catalog release: {error}\n")


if __name__ == "__main__":
    main()
