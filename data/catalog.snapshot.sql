-- Spot Wu Family catalog snapshot
-- Generated deterministically from data/catalog.db
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
DELETE FROM "album_artists";
DELETE FROM "album_tracks";
DELETE FROM "albums";
DELETE FROM "artist_albums";
DELETE FROM "artist_aliases";
DELETE FROM "artist_tracks";
DELETE FROM "artists";
DELETE FROM "configured_artists";
DELETE FROM "copyrights";
DELETE FROM "external_urls";
DELETE FROM "images";
DELETE FROM "schema_migrations";
INSERT INTO "schema_migrations" ("applied_at", "checksum", "name", "version") VALUES ('1970-01-01T00:00:00Z', 'c809ffb0d68c4aec0f0690dae191d7386ce0aebb8efc1f9b161340b61cfce27b', '0001_initial.sql', 1);
DELETE FROM "sync_runs";
DELETE FROM "track_artists";
DELETE FROM "tracks";
COMMIT;
PRAGMA foreign_keys=ON;
