-- Remove the deferrable constraint that's preventing foreign keys
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_unique_id;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_pkey CASCADE;
ALTER TABLE sites ADD PRIMARY KEY (id);
