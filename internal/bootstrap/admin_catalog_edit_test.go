package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/admin/maintenance"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

func newArtistForm() url.Values {
	return url.Values{"slug": {"ghostface-killah"}, "name": {"Ghostface Killah"}, "category": {"core"}, "intent": {"save"}}
}

func TestCatalogEditsWaitForMaintenanceJobsHoldingTheCatalog(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	s := startStack(t, e2eConfig(root, envMap(nil), readyReleaser()))
	catalogPath := filepath.Join(root, "data", "artists.yaml")
	before, err := os.ReadFile(catalogPath)
	require.NoError(t, err)

	started, release := make(chan struct{}), make(chan struct{})
	job, err := s.a.manager.Submit(context.Background(), jobs.Spec{
		Kind: "refresh-metadata", Resources: []string{maintenance.ResourceDatabase, maintenance.ResourceCatalogYAML},
		Run: func(ctx context.Context, _ jobs.Reporter) (jobs.Outcome, error) {
			close(started)
			<-release
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		},
	})
	require.NoError(t, err)
	<-started

	// HTML and JSON callers get a 409 with a friendly message; nothing is written.
	rec := s.post("/artists", newArtistForm())
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "A maintenance job is updating the catalog; try again when it finishes.")
	form := newArtistForm()
	form.Set("csrf_token", s.csrf)
	req := s.request(http.MethodPost, "/artists", form, "http://"+e2eHost)
	require.Equal(t, http.StatusConflict, req.Code)
	after, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "no edit is written while the job holds the catalog")

	// Every catalog-writing call is refused; previews still work.
	_, err = s.a.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeCreate, Input: admin.ArtistInput{Slug: "x", Name: "X", Category: "core"}, DryRun: true})
	require.False(t, errors.Is(err, jobs.ErrConflict), "a dry run writes nothing and is not blocked: %v", err)
	_, err = s.a.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeArchive, Slug: "wu-tang-clan"})
	require.ErrorIs(t, err, jobs.ErrConflict)
	_, err = s.a.ApplyRelationship(context.Background(), admin.RelationshipRequest{Kind: admin.RelRemove})
	require.ErrorIs(t, err, jobs.ErrConflict)
	_, err = s.a.ApplyResolution(context.Background(), admin.ResolveRequest{Slug: "wu-tang-clan", Confirm: true})
	require.ErrorIs(t, err, jobs.ErrConflict)

	close(release)
	require.Equal(t, jobs.StatusSucceeded, s.wait(job.ID).Status)
	rec = s.post("/artists", newArtistForm())
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
}

func TestCatalogEditReleasesTheCatalogOnEveryPath(t *testing.T) {
	root := e2eRoot(t, maintenanceCatalogYAML)
	s := startStack(t, e2eConfig(root, envMap(nil), readyReleaser()))

	// A failing edit (unknown record) must not leave the catalog held.
	_, err := s.a.ApplyChange(context.Background(), admin.ChangeRequest{Kind: admin.ChangeArchive, Slug: "nobody"})
	require.Error(t, err)
	require.False(t, errors.Is(err, jobs.ErrConflict))

	j, err := s.a.manager.Submit(context.Background(), jobs.Spec{Kind: "discover-import", Resources: []string{maintenance.ResourceCatalogYAML},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}})
	require.NoError(t, err, "the catalog is free again after the edit")
	s.wait(j.ID)

	// While an edit holds the catalog, a job needing it is refused.
	release, err := s.a.manager.Acquire(maintenance.ResourceCatalogYAML)
	require.NoError(t, err)
	_, err = s.a.manager.Submit(context.Background(), jobs.Spec{Kind: "discover-import", Resources: []string{maintenance.ResourceCatalogYAML},
		Run: func(context.Context, jobs.Reporter) (jobs.Outcome, error) {
			return jobs.Outcome{Status: jobs.StatusSucceeded}, nil
		}})
	var ce *jobs.ConflictError
	require.ErrorAs(t, err, &ce)
	release()
}
