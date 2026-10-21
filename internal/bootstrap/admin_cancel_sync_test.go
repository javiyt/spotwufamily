package bootstrap

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

// cancelOnFetcher answers every artist, except that the first request for
// blockOn signals entered and waits for the job's cancellation. It records
// the artists it was asked for.
type cancelOnFetcher struct {
	mu      sync.Mutex
	blockOn string
	blocked bool
	entered chan struct{}
	seen    []string
}

func (f *cancelOnFetcher) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *cancelOnFetcher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = nil
}

func (f *cancelOnFetcher) GetArtist(ctx context.Context, id string) (catalog.ArtistCandidate, error) {
	f.mu.Lock()
	f.seen = append(f.seen, id)
	block := id == f.blockOn && !f.blocked
	if block {
		f.blocked = true
	}
	f.mu.Unlock()
	if block {
		close(f.entered)
		<-ctx.Done()
		return catalog.ArtistCandidate{}, ctx.Err()
	}
	return catalog.ArtistCandidate{SpotifyID: id, Name: id}, nil
}

func (*cancelOnFetcher) GetArtistAlbums(context.Context, string, []string) ([]catalog.Release, error) {
	return nil, nil
}

func (*cancelOnFetcher) GetAlbum(context.Context, string) (catalog.Release, error) {
	return catalog.Release{}, nil
}

func (*cancelOnFetcher) GetAlbumTracks(context.Context, string) ([]catalog.Track, error) {
	return nil, nil
}

func TestCancelledWholeCatalogSyncResumesAfterCompletedArtists(t *testing.T) {
	root := e2eRoot(t, threeArtistCatalog)
	fetcher := &cancelOnFetcher{blockOn: "1AAAAAAAAAAAAAAAAAAAAA", entered: make(chan struct{})}
	s := startStack(t, e2eConfig(root, credentials, readyReleaser(), withSyncFetcher(fetcher)))
	all := maintenance.Params{"scope": "all"}

	pv, tok, err := s.a.PreviewAction(context.Background(), "sync", all)
	require.NoError(t, err)
	id := s.startedJob(s.confirmWith("sync", all, tok, pv.RequiresTyped, nil))
	<-fetcher.entered // the first artist is done, the second is in flight
	require.NoError(t, s.a.CancelJob(id))
	first := s.wait(id)
	require.Equal(t, jobs.StatusCancelled, first.Status, first.Message)
	require.True(t, first.Resumable)

	fetcher.reset()
	resume := maintenance.Params{"scope": "all", "resume": "true"}
	pv, tok, err = s.a.PreviewAction(context.Background(), "sync", resume)
	require.NoError(t, err)
	second := s.wait(s.startedJob(s.confirmWith("sync", resume, tok, pv.RequiresTyped, nil)))
	require.Equal(t, jobs.StatusSucceeded, second.Status, second.Message)
	require.Equal(t, []string{"1AAAAAAAAAAAAAAAAAAAAA", "1BBBBBBBBBBBBBBBBBBBBB"}, fetcher.requested(),
		"the resume skips the artist completed before the cancel")
}
