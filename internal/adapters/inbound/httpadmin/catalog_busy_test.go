package httpadmin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/javiyt/spotwufamily/internal/adapters/inbound/httpadmin"
	"github.com/javiyt/spotwufamily/internal/application/admin"
	"github.com/javiyt/spotwufamily/internal/application/jobs"
)

// busyCatalog is the real service with every catalog write refused as the
// composition root refuses it while a maintenance job holds the catalog.
type busyCatalog struct{ admin.Service }

func (b busyCatalog) ApplyChange(ctx context.Context, req admin.ChangeRequest) (admin.ChangeResult, error) {
	if req.DryRun {
		return b.Service.ApplyChange(ctx, req)
	}
	return admin.ChangeResult{}, &jobs.ConflictError{HolderID: "0b5c1f6e-1111-4222-8333-444455556666"}
}

func (b busyCatalog) ApplyRelationship(ctx context.Context, req admin.RelationshipRequest) (admin.RelationshipResult, error) {
	if req.DryRun {
		return b.Service.ApplyRelationship(ctx, req)
	}
	return admin.RelationshipResult{}, &jobs.ConflictError{HolderID: "0b5c1f6e-1111-4222-8333-444455556666"}
}

func (b busyCatalog) ApplyResolution(context.Context, admin.ResolveRequest) (admin.ResolveResult, error) {
	return admin.ResolveResult{}, &jobs.ConflictError{HolderID: "0b5c1f6e-1111-4222-8333-444455556666"}
}

func TestCatalogWritesRefusedWhileAJobHoldsTheCatalog(t *testing.T) {
	env := newResolveEnv(t, func(e *resolveEnv, _ *admin.ResolutionDeps) {
		e.wrap = func(svc admin.Service) httpadmin.Service { return busyCatalog{svc} }
	})
	before := env.file()
	const msg = "A maintenance job is updating the catalog; try again when it finishes."

	rev := env.revision("newcomer")
	for _, rec := range []*httptest.ResponseRecorder{
		env.post("/artists/newcomer", artistForm("newcomer", rev, "name", "Newcomer", "notes", "x", "intent", "save")),
		env.post("/artists/newcomer/archive", artistForm("newcomer", rev)),
		env.post("/resolve/newcomer/apply", decision("newcomer", rzNewcomer, "accept_primary", rev, "confirm", "1")),
	} {
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), msg)
	}

	// JSON callers get the same answer as structured JSON.
	form := artistForm("newcomer", rev, "name", "Newcomer", "intent", "save")
	form.Set("csrf_token", env.csrf)
	req := httptest.NewRequest(http.MethodPost, "/artists/newcomer", strings.NewReader(form.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+host)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"job_running"`)
	require.Contains(t, rec.Body.String(), msg)

	require.Equal(t, before, env.file(), "nothing was written")
}
