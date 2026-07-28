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
		OutputDir: "generated",
		StaticDir: "static",
	})

	require.NoError(t, err)
	require.Equal(t, 1, report.Artists)
	require.Equal(t, 1, report.Albums)
	require.Equal(t, 1, report.Tracks)
	require.NotZero(t, report.FilesWritten)

	require.Contains(t, string(writer.files["generated/catalog-summary.json"]), `"format_version": 1`)
	require.Contains(t, string(writer.files["generated/artists/index.json"]), `"wu-tang-clan"`)
	require.Contains(t, string(writer.files["generated/albums/album-1.json"]), `"Track One"`)
	require.Contains(t, string(writer.files["generated/tracks/track-1.json"]), `"Album One"`)
	require.Contains(t, string(writer.files["static/search-index.json"]), `"type": "artist"`)
}

func TestExportCatalogSecondRunKeepsUnchangedFiles(t *testing.T) {
	writer := &memoryWriter{files: map[string][]byte{}}
	usecase := catalogexport.NewExportCatalog(fakeReader{catalog: sampleCatalog()}, writer)

	_, err := usecase.Run(context.Background(), catalogexport.Options{OutputDir: "generated", StaticDir: "static"})
	require.NoError(t, err)
	report, err := usecase.Run(context.Background(), catalogexport.Options{OutputDir: "generated", StaticDir: "static"})

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
