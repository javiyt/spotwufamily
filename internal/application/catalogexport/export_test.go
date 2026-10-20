package catalogexport_test

import (
	"context"
	"strings"
	"testing"

	"github.com/javiyt/spotwufamily/internal/application/catalogexport"
	"github.com/stretchr/testify/require"
)

func TestExportCatalogWritesDeterministicFiles(t *testing.T) {
	writer := &memoryWriter{files: map[string][]byte{}}
	usecase := catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, writer)

	report, err := usecase.Run(context.Background(), catalogexport.Options{
		OutputDir:  "generated",
		StaticDir:  "static",
		ContentDir: "content/generated",
	})

	require.NoError(t, err)
	require.Equal(t, 1, report.Artists)
	require.Equal(t, 1, report.Albums)
	require.Equal(t, 1, report.Tracks)
	require.NotZero(t, report.FilesWritten)

	require.Contains(t, string(writer.files["generated/catalog-summary.json"]), `"format_version": 1`)
	require.Contains(t, string(writer.files["generated/artists/index.json"]), `"wu-tang-clan"`)
	require.Contains(t, string(writer.files["generated/albums/album-1.json"]), `"Track One"`)
	require.Contains(t, string(writer.files["generated/albums/index.json"]), `"related_artists": [`)
	require.Contains(t, string(writer.files["generated/tracks/track-1.json"]), `"Album One"`)
	require.Contains(t, string(writer.files["generated/tracks/index.json"]), `"release_years": [`)
	require.Contains(t, string(writer.files["generated/tracks/index.json"]), `"groups": [`)
	require.Contains(t, string(writer.files["content/generated/artists/wu-tang-clan.md"]), `layout: "artist"`)
	require.Contains(t, string(writer.files["content/generated/albums/album-1.md"]), `layout: "album"`)
	require.Contains(t, string(writer.files["static/search-index.json"]), `"type": "artist"`)
	require.Contains(t, string(writer.files["static/search-index.json"]), `"url": "/artists/wu-tang-clan/"`)
	require.Contains(t, string(writer.files["static/search-index.json"]), `"url": "/albums/album-1/"`)
	require.Contains(t, string(writer.files["static/search-index.json"]), `"url": "/tracks/#track-1"`)
}

func TestExportCatalogSecondRunKeepsUnchangedFiles(t *testing.T) {
	writer := &memoryWriter{files: map[string][]byte{}}
	usecase := catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, writer)

	_, err := usecase.Run(context.Background(), catalogexport.Options{OutputDir: "generated", StaticDir: "static", ContentDir: "content/generated"})
	require.NoError(t, err)
	report, err := usecase.Run(context.Background(), catalogexport.Options{OutputDir: "generated", StaticDir: "static", ContentDir: "content/generated"})

	require.NoError(t, err)
	require.Zero(t, report.FilesWritten)
	require.NotZero(t, report.FilesKept)
}

type fakeReader struct {
	catalog catalogexport.Catalog
}

func (f fakeReader) LoadExportCatalog(context.Context) (catalogexport.Catalog, error) {
	return f.catalog, nil
}

type memoryWriter struct {
	files map[string][]byte
}

func (m *memoryWriter) WriteFile(path string, data []byte) (bool, error) {
	if existing, ok := m.files[path]; ok && string(existing) == string(data) {
		return false, nil
	}
	m.files[path] = append([]byte(nil), data...)

	return true, nil
}

func (m *memoryWriter) PruneDir(dir string, keep map[string]struct{}) error {
	for path := range m.files {
		if !strings.HasPrefix(path, dir+"/") {
			continue
		}
		if _, ok := keep[path]; !ok {
			delete(m.files, path)
		}
	}

	return nil
}

func sampleCatalog() catalogexport.Catalog {
	return catalogexport.Catalog{
		Stats: catalogexport.Stats{Artists: 1, Groups: 1, Albums: 1, Tracks: 1, LastSyncRunID: 7},
		Artists: []catalogexport.Artist{{
			Slug:          "wu-tang-clan",
			Name:          "Wu-Tang Clan",
			SpotifyID:     "artist-1",
			Category:      "core",
			Aliases:       []string{"Wu Tang Clan"},
			Enabled:       true,
			ReleaseCount:  1,
			TrackCount:    1,
			EditorialRank: 1,
		}},
		Albums: []catalogexport.Album{{
			SpotifyID:   "album-1",
			Name:        "Album One",
			AlbumType:   "album",
			ReleaseDate: "1993-11-09",
			TotalTracks: 1,
			Artists:     []catalogexport.Credit{{SpotifyID: "artist-1", Name: "Wu-Tang Clan"}},
			Related:     []catalogexport.GroupCredit{{Slug: "wu-tang-clan", SpotifyID: "artist-1", Name: "Wu-Tang Clan", Category: "core"}},
			Tracks: []catalogexport.AlbumTrack{{
				SpotifyID:   "track-1",
				Name:        "Track One",
				DiscNumber:  1,
				TrackNumber: 1,
				DurationMS:  120000,
				Artists:     []catalogexport.Credit{{SpotifyID: "artist-1", Name: "Wu-Tang Clan"}},
			}},
		}},
		Tracks: []catalogexport.Track{{
			SpotifyID:  "track-1",
			Name:       "Track One",
			DurationMS: 120000,
			Artists:    []catalogexport.Credit{{SpotifyID: "artist-1", Name: "Wu-Tang Clan"}},
			Albums:     []catalogexport.Credit{{SpotifyID: "album-1", Name: "Album One"}},
		}},
	}
}

func TestExportCatalogWritesSortedRelationships(t *testing.T) {
	rels := []catalogexport.Relationship{
		{Type: "member_of", From: "rza", To: "wu-tang-clan", Confidence: "high"},
		{Type: "associated_with", From: "a", To: "b"},
		{Type: "member_of", From: "gza", To: "wu-tang-clan", End: "2000", Former: true},
	}
	writer := &memoryWriter{files: map[string][]byte{}}
	opts := catalogexport.Options{OutputDir: "generated", StaticDir: "static", ContentDir: "content/generated", Relationships: rels}
	_, err := catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, writer).Run(context.Background(), opts)
	require.NoError(t, err)

	out := string(writer.files["generated/relationships/index.json"])
	require.Less(t, strings.Index(out, `"associated_with"`), strings.Index(out, `"gza"`))
	require.Less(t, strings.Index(out, `"gza"`), strings.Index(out, `"rza"`))
	require.Contains(t, out, `"former": true`)

	// Same input in another order produces byte-identical output.
	reordered := []catalogexport.Relationship{rels[2], rels[1], rels[0]}
	opts.Relationships = reordered
	other := &memoryWriter{files: map[string][]byte{}}
	_, err = catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, other).Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, out, string(other.files["generated/relationships/index.json"]))
}

func TestExportCatalogWritesEmptyRelationshipsForLegacyCatalogs(t *testing.T) {
	writer := &memoryWriter{files: map[string][]byte{}}
	_, err := catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, writer).Run(context.Background(), catalogexport.Options{OutputDir: "generated", StaticDir: "static", ContentDir: "content/generated"})
	require.NoError(t, err)
	require.Contains(t, string(writer.files["generated/relationships/index.json"]), `"items": []`)
}

func TestExportCatalogPublishesPendingReconciliationsReadOnly(t *testing.T) {
	cat := sampleCatalog()
	cat.Reconciliations = []catalogexport.PendingReconciliation{{Slug: "streetlife", Status: "drifted", PreviousIDs: []string{"old"}, TargetIDs: []string{"new"}}}
	writer := &memoryWriter{files: map[string][]byte{}}
	opts := catalogexport.Options{OutputDir: "generated", StaticDir: "static", ContentDir: "content/generated"}
	_, err := catalogexport.NewExportCatalog(fakeReader{catalog: cat}, writer).Run(context.Background(), opts)
	require.NoError(t, err)
	out := string(writer.files["generated/reconciliation/index.json"])
	require.Contains(t, out, `"slug": "streetlife"`)
	require.Contains(t, out, `"status": "drifted"`)

	empty := &memoryWriter{files: map[string][]byte{}}
	_, err = catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, empty).Run(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, string(empty.files["generated/reconciliation/index.json"]), `"items": []`)
}
