-- Queue records without a predecessor cannot be represented by the old
-- binary. Restore the pre-upgrade database backup instead of downgrading.
SELECT oktapod_downgrade_unsupported_restore_pre_upgrade_backup();
