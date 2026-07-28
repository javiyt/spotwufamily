# Database

The catalog database will live at `data/catalog.db` and will be versioned in Git.

Planned constraints:

- SQL migrations in `migrations/`.
- `PRAGMA foreign_keys = ON`.
- Deterministic writes where practical.
- Temporary SQLite files are ignored: `data/catalog.db-wal`, `data/catalog.db-shm`, `data/catalog.db-journal`.
- A logical snapshot will be generated beside the binary database for reviewable pull requests.
