package db

// NewMigrator lets the integration tests shorten the lock waits, and take the lock between
// preparing the migrations and applying them.
var NewMigrator = newMigrator
