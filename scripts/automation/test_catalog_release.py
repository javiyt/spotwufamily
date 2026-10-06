import gzip
import importlib.util
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("catalog_release", Path(__file__).with_name("catalog-release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class CatalogReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.package = self.root / "package"
        self.package.mkdir()
        self.source = self.root / "source.db"
        self.connection = sqlite3.connect(self.source)
        self.addCleanup(self.connection.close)
        self.connection.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE albums (id TEXT PRIMARY KEY, title TEXT);
            INSERT INTO albums VALUES ('album', 'Preserved music');
            CREATE TABLE artist_metadata_refreshes (artist TEXT PRIMARY KEY, checkpoint TEXT);
            INSERT INTO artist_metadata_refreshes VALUES ('artist', 'Preserved checkpoint');
        """)

    def prepare(self):
        import argparse
        args = argparse.Namespace(output=str(self.package), db=str(self.source), tag="catalog-test", repo="javiyt/spotwufamily")
        # The production CLI generates/validates project-specific snapshots;
        # this fixture verifies storage independently of that schema.
        def cli(*args):
            if "snapshot" in args:
                path = Path(args[args.index("--snapshot") + 1])
                with gzip.open(path, "wb") as stream:
                    stream.write(b"-- fixture snapshot\n")
        with patch.object(release, "run", side_effect=cli):
            release.prepare(args)
        return release.read_manifest(self.package / "catalog.release.json", args.repo)

    def test_backup_restores_all_tables_including_committed_wal(self):
        manifest = self.prepare()
        restored = self.root / "restored.db"
        release.install(self.package, manifest, restored, self.root / "snapshot.gz")
        with sqlite3.connect(restored) as db:
            self.assertEqual(db.execute("SELECT title FROM albums").fetchone(), ("Preserved music",))
            self.assertEqual(db.execute("SELECT checkpoint FROM artist_metadata_refreshes").fetchone(), ("Preserved checkpoint",))
        self.assertEqual(release.content_hash(restored), release.content_hash(self.source))

    def test_corrupt_download_never_replaces_local_catalog(self):
        manifest = self.prepare()
        restored = self.root / "restored.db"
        restored.write_bytes(b"local data")
        snapshot = self.root / "snapshot.gz"
        snapshot.write_bytes(b"local snapshot")
        with (self.package / "catalog.db.gz").open("ab") as stream:
            stream.write(b"corruption")
        with self.assertRaisesRegex(ValueError, "checksum/size"):
            release.install(self.package, manifest, restored, snapshot, force=True)
        self.assertEqual(restored.read_bytes(), b"local data")
        self.assertEqual(snapshot.read_bytes(), b"local snapshot")

    def test_existing_catalog_requires_explicit_force(self):
        manifest = self.prepare()
        existing = self.root / "existing.db"
        existing.write_bytes(b"local catalog")
        with self.assertRaisesRegex(ValueError, "Local catalog exists"):
            release.install(self.package, manifest, existing, self.root / "snapshot.gz")

    def test_sidecars_block_even_forced_replacement(self):
        manifest = self.prepare()
        (self.root / "restored.db-wal").touch()
        with self.assertRaisesRegex(ValueError, "Close SQLite"):
            release.install(self.package, manifest, self.root / "restored.db", self.root / "snapshot.gz", True)

    def test_failed_upload_keeps_git_pointer(self):
        import argparse
        import subprocess
        self.prepare()
        pointer = self.root / "pointer.json"
        args = argparse.Namespace(output=str(self.package), repo="javiyt/spotwufamily", manifest=str(pointer), target="main")
        with patch.object(release, "run", side_effect=subprocess.CalledProcessError(1, "gh")):
            with self.assertRaises(subprocess.CalledProcessError):
                release.publish(args)
        self.assertFalse(pointer.exists())

    def test_unchanged_content_skips_upload(self):
        import argparse
        manifest = self.prepare()
        pointer = self.root / "pointer.json"
        release.write_json(pointer, manifest)
        args = argparse.Namespace(output=str(self.package), repo="javiyt/spotwufamily", manifest=str(pointer), target="main")
        with patch.object(release, "run") as run:
            release.publish(args)
        run.assert_not_called()

    def test_manifest_rejects_cross_repository_and_unexpected_asset_paths(self):
        manifest = self.prepare()
        path = self.package / "catalog.release.json"
        with self.assertRaisesRegex(ValueError, "repository"):
            release.read_manifest(path, "other/repository")
        manifest["assets"]["../../catalog.db"] = manifest["assets"].pop("catalog.db.gz")
        release.write_json(path, manifest)
        with self.assertRaisesRegex(ValueError, "Unexpected release assets"):
            release.read_manifest(path, "javiyt/spotwufamily")


if __name__ == "__main__":
    unittest.main()
