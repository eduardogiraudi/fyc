package tools

// D1: the dedicated-user sandbox — user-facing surface.
//
// This file holds the platform-neutral types plus the thin dispatchers; the
// Windows implementation lives in sandbox_users_windows.go and the no-op stub
// in sandbox_users_other.go. Kept build-tag free so main.go can offer
// `fyc sandbox setup|status|reset` on every OS without a build tag of its own
// (same pattern as sandbox.go's SandboxStatus/SandboxReset wrappers).

// SandboxSetupOptions drives `fyc sandbox setup`.
type SandboxSetupOptions struct {
	// DryRun computes the plan (and the resulting status) without touching the
	// system: no user, no group, no ACEs. Safe to run unelevated.
	DryRun bool
	// Force re-runs the setup even when the marker already records the current
	// version (the idempotency short-circuit is skipped).
	Force bool
	// Workspace is the sandbox's root at setup time. Its sensitive paths
	// (<ws>/.git, …) join the deny-list so they stay hidden inside the granted
	// profile (the runtime re-plants them per command in D2). Empty = none.
	Workspace string
	// Log receives one line per step; nil means "silent". The same lines always
	// go to the setup log file.
	Log func(string)
}

// SandboxSetupResult reports what `fyc sandbox setup` did.
type SandboxSetupResult struct {
	Platform    string
	Supported   bool
	AlreadyDone bool // marker already at the current version: nothing to do
	ReLaunched  bool // unelevated: the work was handed to an elevated copy
	Elevated    bool
	User        string
	Group       string
	Version     int
	SecretsPath string
	LogPath     string
	Notes       []string
}

// SandboxUsersStatus is the D1 slice of `fyc sandbox status`.
type SandboxUsersStatus struct {
	Platform       string
	Supported      bool
	Elevated       bool
	User           string
	UserExists     bool
	Group          string
	GroupExists    bool
	MemberOfGroup  bool
	SetupVersion   int
	Ready          bool // user+group+membership+marker+credentials all present
	SecretsPath    string
	SecretsPresent bool
	LogPath        string
	Err            string
}

// SandboxUsersResetResult reports what `fyc sandbox reset` removed.
type SandboxUsersResetResult struct {
	Platform     string
	Supported    bool
	UserDeleted  bool
	GroupDeleted bool
	StrippedACLs int
	RemovedFiles []string
	Notes        []string
	Err          string
}

// SandboxSetup installs the dedicated-user sandbox. It is
// idempotent; on Windows it elevates itself through UAC when needed and returns
// once the elevated copy finished. On other platforms it reports Supported=false.
func SandboxSetup(opts SandboxSetupOptions) (SandboxSetupResult, error) {
	return sandboxSetup(opts)
}

// SandboxSetupStatus reports the state of the dedicated-user sandbox for the
// given workspace (the workspace no longer contributes anything: the setup
// plants nothing on the filesystem beyond the credentials DACL).
func SandboxSetupStatus(workspace string) SandboxUsersStatus {
	return sandboxUsersStatus(workspace)
}

// SandboxUsersReset undoes the setup: user, group, credentials, marker and the
// read-ACL/deny ACEs a previous setup left behind. On other platforms it is a no-op.
func SandboxUsersReset() (SandboxUsersResetResult, error) {
	return sandboxUsersReset()
}
