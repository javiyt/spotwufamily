CREATE TABLE images_new (
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  url TEXT NOT NULL,
  height INTEGER,
  width INTEGER,
  position INTEGER NOT NULL,
  PRIMARY KEY (owner_type, owner_id, position)
);

INSERT INTO images_new(owner_type, owner_id, url, height, width, position)
SELECT owner_type, owner_id, url, height, width, position
FROM images
WHERE rowid IN (
  SELECT MAX(rowid)
  FROM images
  GROUP BY owner_type, owner_id, position
);

DROP TABLE images;
ALTER TABLE images_new RENAME TO images;
