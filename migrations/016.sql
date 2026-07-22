ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS pickup_length SMALLINT NOT NULL DEFAULT 6,
    ADD COLUMN IF NOT EXISTS pickup_case_sensitive BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS pickup_include_letters BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS pickup_include_numbers BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS pickup_max_lifetime_seconds BIGINT DEFAULT 3600;
ALTER TABLE system_settings ADD CONSTRAINT system_settings_pickup_length CHECK (pickup_length BETWEEN 1 AND 64);
ALTER TABLE system_settings ADD CONSTRAINT system_settings_pickup_charset CHECK (pickup_include_letters OR pickup_include_numbers);
ALTER TABLE system_settings ADD CONSTRAINT system_settings_pickup_lifetime CHECK (pickup_max_lifetime_seconds IS NULL OR pickup_max_lifetime_seconds > 0);

ALTER TABLE shares
    ADD COLUMN IF NOT EXISTS share_type TEXT NOT NULL DEFAULT 'link',
    ADD COLUMN IF NOT EXISTS pickup_code TEXT,
    ADD COLUMN IF NOT EXISTS pickup_case_sensitive BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE shares ADD CONSTRAINT shares_type_valid CHECK (share_type IN ('link', 'pickup'));
ALTER TABLE shares ADD CONSTRAINT shares_pickup_shape CHECK ((share_type='link' AND pickup_code IS NULL) OR (share_type='pickup' AND pickup_code IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS shares_pickup_code_unique ON shares(pickup_code) WHERE pickup_code IS NOT NULL;
