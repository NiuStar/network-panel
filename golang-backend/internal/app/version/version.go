package version

// serverVersion follows SemVer string for the backend.
// Agent expected version is derived from this (see controller/version.go).
var serverVersion = "1.0.9.3"

func Get() string { return serverVersion }
