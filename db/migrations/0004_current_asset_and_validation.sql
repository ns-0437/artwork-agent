-- Tracks which asset is "current" for inspection/proof purposes, separate
-- from "the most recent upload of kind='original'". A successful repair
-- must make the REPAIRED asset current - otherwise a later startResolution
-- (or future proof generation) silently falls back to the pristine
-- original and can reintroduce a blocker the repair just cleared (e.g. the
-- missing-bleed finding). Set by RecordArtworkUpload (to the new upload)
-- and by CompleteRepair (to the new repaired asset) - both atomically with
-- everything else those transactions do.
ALTER TABLE orders ADD COLUMN current_asset_id UUID REFERENCES assets(id);

-- A zero, negative, or missing declared size breaks the PPI math in
-- services/image-python (division by a non-positive number) - reject it at
-- the source rather than trusting every caller to check.
ALTER TABLE orders ADD CONSTRAINT declared_width_positive CHECK (declared_width > 0);
ALTER TABLE orders ADD CONSTRAINT declared_height_positive CHECK (declared_height > 0);
