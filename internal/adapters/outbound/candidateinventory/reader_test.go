package candidateinventory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/javiyt/spotwufamily/internal/adapters/outbound/candidateinventory"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestReadCSVWithBOMSpanishHeadersAndQuotedFields(t *testing.T) {
	path := write(t, "faltantes.csv", "\xef\xbb\xbftipo,nombre,nivel_verificacion_referencia,funciones,relaciones_a_grupos,fuentes_ids,criterio\n"+
		"artista,Frukwan,alta,rapero_o_colaborador,group-gravediggaz,gravediggaz,\"No figura, ni variante\"\n"+
		"artista,\"Ol’ Dírty \"\"Bastard\"\"\",media,productor,\"group-a, group-b\",\"x, y\",\n"+
		"grupo,Black Brigade,baja,,,directorio_fans,\n")

	inputs, err := candidateinventory.NewReader().ReadCandidates(context.Background(), path)

	require.NoError(t, err)
	require.Len(t, inputs, 3)
	require.Equal(t, "Frukwan", inputs[0].Name)
	require.Equal(t, "alta", inputs[0].Confidence)
	require.Equal(t, "group-gravediggaz", inputs[0].RelationClaim)
	require.Equal(t, "gravediggaz", inputs[0].EvidenceCategory)
	require.Equal(t, "No figura, ni variante", inputs[0].Note)
	require.Equal(t, "faltantes.csv", inputs[0].SourceTitle)
	require.Equal(t, `Ol’ Dírty "Bastard"`, inputs[1].Name)
	require.Equal(t, []string{"productor"}, inputs[1].Functions)
	require.Equal(t, "group-a, group-b", inputs[1].RelationClaim)
	require.Equal(t, "grupo", inputs[2].Kind)
}

func TestReadCSVNeutralColumnsAndExternalIDs(t *testing.T) {
	path := write(t, "n.csv", "name,kind,aliases,confidence,evidence_category,relation_claim,source_title,source_url,proposed_slug,proposed_category,alias_of,external_ids\n"+
		"Kévlaar 7,artist,Kevlaar Seven;K7,high,wisemen,member of Wisemen,Wisemen article,https://en.wikipedia.org/wiki/Wisemen_(rap_group),kevlaar-7,producer,,musicbrainz=5c30c7b8-920e-4f55-aa84-ef61611260cc\n")

	inputs, err := candidateinventory.NewReader().ReadCandidates(context.Background(), path)

	require.NoError(t, err)
	require.Len(t, inputs, 1)
	got := inputs[0]
	require.Equal(t, []string{"Kevlaar Seven", "K7"}, got.Aliases)
	require.Equal(t, "Wisemen article", got.SourceTitle)
	require.Equal(t, "https://en.wikipedia.org/wiki/Wisemen_(rap_group)", got.SourceURL)
	require.Equal(t, "producer", got.ProposedCategory)
	require.Equal(t, map[string]string{"musicbrainz": "5c30c7b8-920e-4f55-aa84-ef61611260cc"}, got.ExternalIDs)
}

func TestReadJSONAuditDocumentIncludingVariantClaims(t *testing.T) {
	path := write(t, "audit.json", `{
		"origen_referencia": "wu_family.json",
		"origen_catalogo": "https://example.test/catalog",
		"variantes_equivalentes": {"The Wisemen": "Wisemen", "Ancient Coins": "Ancients Coins"},
		"grupos_ausentes": [{"tipo": "grupo", "nombre": "The Purge", "nivel_verificacion_referencia": "baja", "fuentes_ids": "directorio_fans"}],
		"artistas_ausentes": [{"tipo": "artista", "nombre": "Myalansky", "nivel_verificacion_referencia": "alta", "funciones": "rapero_o_colaborador", "relaciones_a_grupos": "group-wu-syndicate"}]
	}`)

	inputs, err := candidateinventory.NewReader().ReadCandidates(context.Background(), path)

	require.NoError(t, err)
	require.Len(t, inputs, 4)
	require.Equal(t, "The Purge", inputs[0].Name)
	require.Equal(t, "wu_family.json", inputs[0].SourceTitle)
	require.Equal(t, "Myalansky", inputs[1].Name)
	// Variants are sorted by name and keep the unverified claim.
	require.Equal(t, "Ancient Coins", inputs[2].Name)
	require.Equal(t, "Ancients Coins", inputs[2].AliasOf)
	require.Equal(t, "The Wisemen", inputs[3].Name)
}

func TestReadJSONNeutralDocumentAndBareArray(t *testing.T) {
	neutral := write(t, "neutral.json", `{"source":{"title":"Curated","url":"https://example.test/s"},"candidates":[{"name":"Champ MC","kind":"artist","aliases":["Champ"],"confidence":"high","external_ids":{"musicbrainz":"abc"}}]}`)
	bare := write(t, "bare.json", `[{"name":"N-Tyce","kind":"artist"}]`)

	a, err := candidateinventory.NewReader().ReadCandidates(context.Background(), neutral)
	require.NoError(t, err)
	require.Equal(t, "Curated", a[0].SourceTitle)
	require.Equal(t, "https://example.test/s", a[0].SourceURL)
	require.Equal(t, []string{"Champ"}, a[0].Aliases)
	require.Equal(t, map[string]string{"musicbrainz": "abc"}, a[0].ExternalIDs)

	b, err := candidateinventory.NewReader().ReadCandidates(context.Background(), bare)
	require.NoError(t, err)
	require.Equal(t, "bare.json", b[0].SourceTitle)
}

func TestReadRejectsUnsupportedAndMalformedFiles(t *testing.T) {
	r := candidateinventory.NewReader()
	_, err := r.ReadCandidates(context.Background(), write(t, "x.txt", "a"))
	require.ErrorContains(t, err, "unsupported")
	_, err = r.ReadCandidates(context.Background(), write(t, "bad.json", "{"))
	require.ErrorContains(t, err, "parse JSON")
	_, err = r.ReadCandidates(context.Background(), write(t, "bad2.json", `{"candidates":["x"]}`))
	require.ErrorContains(t, err, "must be objects")
	_, err = r.ReadCandidates(context.Background(), filepath.Join(t.TempDir(), "missing.csv"))
	require.Error(t, err)
	empty, err := r.ReadCandidates(context.Background(), write(t, "empty.csv", ""))
	require.NoError(t, err)
	require.Empty(t, empty)
}
