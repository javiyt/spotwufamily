-- Verified editorial relationships between configured artists. Only reviewed
-- (verified) claims are projected; proposed and rejected ones stay in the YAML.
CREATE TABLE artist_relationships (
  relationship_type TEXT NOT NULL,
  from_slug TEXT NOT NULL,
  to_slug TEXT NOT NULL,
  start_date TEXT NOT NULL DEFAULT '',
  end_date TEXT NOT NULL DEFAULT '',
  era TEXT NOT NULL DEFAULT '',
  confidence TEXT NOT NULL DEFAULT '',
  commentary TEXT NOT NULL DEFAULT '',
  reviewer TEXT NOT NULL DEFAULT '',
  reviewed_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (relationship_type, from_slug, to_slug),
  CHECK (from_slug <> to_slug),
  FOREIGN KEY (from_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE,
  FOREIGN KEY (to_slug) REFERENCES configured_artists(slug) ON DELETE CASCADE
);

CREATE INDEX idx_artist_relationships_to ON artist_relationships(to_slug, relationship_type);

CREATE TABLE artist_relationship_evidence (
  relationship_type TEXT NOT NULL,
  from_slug TEXT NOT NULL,
  to_slug TEXT NOT NULL,
  position INTEGER NOT NULL,
  evidence_type TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  rationale TEXT NOT NULL,
  PRIMARY KEY (relationship_type, from_slug, to_slug, position),
  FOREIGN KEY (relationship_type, from_slug, to_slug)
    REFERENCES artist_relationships(relationship_type, from_slug, to_slug) ON DELETE CASCADE
);
