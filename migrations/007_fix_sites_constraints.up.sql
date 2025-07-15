-- Remove the deferrable constraint that's preventing foreign keys
ALTER TABLE sites DROP CONSTRAINT sites_unique_id;
ALTER TABLE sites DROP CONSTRAINT sites_pkey;
ALTER TABLE sites ADD PRIMARY KEY (id);