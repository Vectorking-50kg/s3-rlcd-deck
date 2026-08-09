# Commit device configuration transactionally

Store versioned device configuration as a candidate record and make it active only after validation succeeds, retaining the last valid record when parsing, migration, or connection verification fails. This uses more storage and state than direct NVS updates, but prevents incomplete Wi-Fi or calibration writes from destroying the Deck’s known recovery path.
