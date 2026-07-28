ALTER TABLE schedules ADD COLUMN misfire_policy TEXT NOT NULL DEFAULT 'ask';
ALTER TABLE schedules ADD COLUMN overlap_policy TEXT NOT NULL DEFAULT 'forbid';
ALTER TABLE schedules ADD COLUMN misfire_grace_seconds INTEGER NOT NULL DEFAULT 300;
