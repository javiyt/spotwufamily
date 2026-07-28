package jsoncandidates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/javiyt/spotwufamily/internal/domain/catalog"
)

type Searcher struct {
	candidates map[string][]catalog.ArtistCandidate
}

func NewSearcher(path string) (Searcher, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Searcher{}, fmt.Errorf("read candidates %s: %w", path, err)
	}

	var candidates map[string][]catalog.ArtistCandidate
	if err := json.Unmarshal(data, &candidates); err != nil {
		return Searcher{}, fmt.Errorf("parse candidates %s: %w", path, err)
	}

	return Searcher{candidates: candidates}, nil
}

func (s Searcher) SearchArtistCandidates(ctx context.Context, artist catalog.Artist) ([]catalog.ArtistCandidate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	return append([]catalog.ArtistCandidate(nil), s.candidates[artist.Slug]...), nil
}
