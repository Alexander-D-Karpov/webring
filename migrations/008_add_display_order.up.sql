ALTER TABLE sites ADD COLUMN display_order INTEGER;

-- Initialize display_order with current ID values
UPDATE sites SET display_order = id;

ALTER TABLE sites ALTER COLUMN display_order SET NOT NULL;
CREATE UNIQUE INDEX idx_sites_display_order ON sites(display_order);