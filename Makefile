.DEFAULT_GOAL := help

.PHONY: help setup format lint test test-race version validate artists-validate artists-import-groups artists-enable-with-ids artists-enable-with-ids-dry-run artists-discover-wu artists-discover-wu-apply artists-refresh-metadata artists-refresh-metadata-dry-run artists-refresh-genres artists-refresh-genres-dry-run artists-seed-db artists-resolve-report artists-resolve-apply artists-resolve-interactive artists-review-interactive artists-resolve-offline artists-audit-albums sync sync-dry-run sync-artist export build serve audit audit-fast db-verify db-migrate db-snapshot db-rebuild site-build init-from-yaml refresh-from-spotify ci

CLI := ./cmd/spotwufamily
BUILD_DIR := build
CATALOG ?= data/artists.yaml
GROUPS ?= data/groups.txt
DB ?= data/catalog.db
SNAPSHOT ?= data/catalog.snapshot.sql
EXPORT_DIR ?= site/data/generated
STATIC_DIR ?= site/static
CANDIDATES ?= data/artist-candidates.example.json
REPORT ?= resolve.md
ALBUM_REPORT ?= albums-audit.md
DISCOVERY_REPORT ?= wu-discovery.md
ARTIST ?= wu-tang-clan
MARKET ?= ES
SITE_SOURCE ?= site
SITE_DESTINATION ?= /tmp/spotwufamily-site

help:
	@printf 'Spot Wu Family targets:\n'
	@printf '  make init-from-yaml          Validate YAML, prepare DB, export JSON, build site, audit\n'
	@printf '  make refresh-from-spotify    Resolve strong IDs, sync one artist, snapshot/export/audit\n'
	@printf '  make artists-resolve-apply   Apply strong Spotify ID matches to YAML\n'
	@printf '  make artists-enable-with-ids Enable YAML artists that have Spotify IDs\n'
	@printf '  make artists-discover-wu    Discover possible Wu Family artists from Wikipedia\n'
	@printf '  make artists-discover-wu-apply Add discovered artists to YAML disabled\n'
	@printf '  make artists-refresh-metadata Refresh YAML Spotify genres, links and images\n'
	@printf '  make artists-seed-db         Seed configured artists from YAML into SQLite\n'
	@printf '  make artists-resolve-interactive  Pick Spotify IDs interactively\n'
	@printf '  make artists-review-interactive   Review all artists, including existing Spotify IDs\n'
	@printf '  make artists-resolve-offline Generate resolve report from local candidate fixture\n'
	@printf '  make artists-audit-albums ARTIST=slug Compare Spotify albums with MusicBrainz\n'
	@printf '  make sync-artist ARTIST=slug Sync one enabled artist from Spotify\n'
	@printf '  make ci                      Local CI gate\n'
	@printf '\nVariables: CATALOG=%s DB=%s SNAPSHOT=%s ARTIST=%s MARKET=%s REPORT=%s ALBUM_REPORT=%s DISCOVERY_REPORT=%s\n' "$(CATALOG)" "$(DB)" "$(SNAPSHOT)" "$(ARTIST)" "$(MARKET)" "$(REPORT)" "$(ALBUM_REPORT)" "$(DISCOVERY_REPORT)"

setup:
	go mod download

format:
	gofmt -w cmd internal

lint:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

version:
	go run $(CLI) version

validate: artists-validate

artists-validate:
	go run $(CLI) artists validate $(CATALOG)

artists-import-groups:
	go run $(CLI) artists import-groups $(GROUPS) $(CATALOG)

artists-enable-with-ids:
	go run $(CLI) artists enable-with-ids --catalog $(CATALOG)

artists-enable-with-ids-dry-run:
	go run $(CLI) artists enable-with-ids --catalog $(CATALOG) --dry-run

artists-discover-wu:
	go run $(CLI) artists discover-wu --catalog $(CATALOG) --report $(DISCOVERY_REPORT)

artists-discover-wu-apply:
	go run $(CLI) artists discover-wu --catalog $(CATALOG) --report $(DISCOVERY_REPORT) --apply

artists-refresh-metadata:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists refresh-genres --catalog $(CATALOG) --market $(MARKET)

artists-refresh-metadata-dry-run:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists refresh-genres --catalog $(CATALOG) --market $(MARKET) --dry-run

artists-refresh-genres:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists refresh-genres --catalog $(CATALOG) --market $(MARKET)

artists-refresh-genres-dry-run:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists refresh-genres --catalog $(CATALOG) --market $(MARKET) --dry-run

artists-seed-db:
	go run $(CLI) artists seed-db --catalog $(CATALOG) --db $(DB)

artists-resolve-report:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists resolve --non-interactive --catalog $(CATALOG) --market $(MARKET) --report $(REPORT)

artists-resolve-apply:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists resolve --non-interactive --apply --catalog $(CATALOG) --market $(MARKET) --report $(REPORT)

artists-resolve-interactive:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists resolve --interactive --catalog $(CATALOG) --market $(MARKET)

artists-review-interactive:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists resolve --interactive --review-all --catalog $(CATALOG) --market $(MARKET)

artists-resolve-offline:
	go run $(CLI) artists resolve --non-interactive --catalog $(CATALOG) --candidates $(CANDIDATES) --report $(REPORT)

artists-audit-albums:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) artists audit-albums --artist $(ARTIST) --catalog $(CATALOG) --market $(MARKET) --report $(ALBUM_REPORT)

sync:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) sync --catalog $(CATALOG) --db $(DB) --snapshot $(SNAPSHOT) --market $(MARKET)

sync-dry-run:
	go run $(CLI) sync --dry-run --catalog $(CATALOG) --market $(MARKET)

sync-artist:
	SPOTIFY_MARKET=$(MARKET) go run $(CLI) sync --artist $(ARTIST) --catalog $(CATALOG) --db $(DB) --snapshot $(SNAPSHOT) --market $(MARKET)

export:
	go run $(CLI) export --db $(DB) --output $(EXPORT_DIR) --static $(STATIC_DIR)

build:
	go build -o $(BUILD_DIR)/spotwufamily $(CLI)

serve:
	hugo server --source site --bind 127.0.0.1

audit:
	go run $(CLI) audit --catalog $(CATALOG) --db $(DB) --snapshot $(SNAPSHOT) --output $(EXPORT_DIR) --static $(STATIC_DIR) --site-source $(SITE_SOURCE) --site-destination $(SITE_DESTINATION) --skip-git-diff

audit-fast:
	go run $(CLI) audit --catalog $(CATALOG) --db $(DB) --snapshot $(SNAPSHOT) --output $(EXPORT_DIR) --static $(STATIC_DIR) --skip-site --skip-git-diff

db-verify:
	go run $(CLI) db verify --db $(DB) --snapshot $(SNAPSHOT)

db-migrate:
	go run $(CLI) db migrate --db $(DB) --snapshot $(SNAPSHOT)

db-snapshot:
	go run $(CLI) db snapshot --db $(DB) --snapshot $(SNAPSHOT)

db-rebuild:
	go run $(CLI) db rebuild --db $(DB) --snapshot $(SNAPSHOT)

site-build:
	go run $(CLI) site build --source $(SITE_SOURCE) --destination $(SITE_DESTINATION)

init-from-yaml: artists-validate db-migrate artists-seed-db db-snapshot db-verify export site-build audit-fast

refresh-from-spotify: artists-resolve-apply artists-validate sync-artist db-verify export audit

ci: format artists-validate db-verify export site-build audit-fast test lint build
