package version

// serverVersion follows SemVer string for the backend.
// Agent expected version is derived from this (see controller/version.go).
var serverVersion = "2.0.0.1"

func Get() string { return serverVersion }
