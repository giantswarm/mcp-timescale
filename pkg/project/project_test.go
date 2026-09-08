package project

import "testing"

// stub replaces the three ldflag-stamped identifiers and the build-info reader
// for one test, restoring them afterwards.
func stub(t *testing.T, v, sha, ts, buildInfo string) {
	t.Helper()
	prevV, prevSHA, prevTS, prevBI := version, gitSHA, buildTimestamp, buildInfoVersion
	version, gitSHA, buildTimestamp = v, sha, ts
	buildInfoVersion = func() string { return buildInfo }
	t.Cleanup(func() {
		version, gitSHA, buildTimestamp, buildInfoVersion = prevV, prevSHA, prevTS, prevBI
	})
}

func TestVersionPrefersTheStampedLdflag(t *testing.T) {
	stub(t, "1.2.3", "abc123", "2026-09-08T10:00:00Z", "v9.9.9")
	if got := Version(); got != "1.2.3" {
		t.Errorf("Version() = %q, want the -X stamped 1.2.3", got)
	}
	if GitSHA() != "abc123" || BuildTimestamp() != "2026-09-08T10:00:00Z" {
		t.Errorf("GitSHA()/BuildTimestamp() = %q/%q", GitSHA(), BuildTimestamp())
	}
}

func TestVersionFallsBackToBuildInfoThenSHAThenDev(t *testing.T) {
	stub(t, dev, dev, "unknown", "v0.2.0")
	if got := Version(); got != "v0.2.0" {
		t.Errorf("without ldflags Version() should read the VCS build info, got %q", got)
	}

	stub(t, dev, "deadbeef", "unknown", "")
	if got := Version(); got != "deadbeef" {
		t.Errorf("without build info Version() should fall back to the commit SHA, got %q", got)
	}

	stub(t, dev, dev, "unknown", "")
	if got := Version(); got != dev {
		t.Errorf("a bare go build should report %q, got %q", dev, got)
	}
}
