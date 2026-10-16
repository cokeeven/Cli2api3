package update

import (
	"context"
	"testing"
)

type releaseSourceStub struct {
	releases []Release
	err      error
	calls    int
}

func (s *releaseSourceStub) ListReleases(context.Context) ([]Release, error) {
	s.calls++
	return s.releases, s.err
}

func TestCheckerTargetsLatestReleaseAndReportsSkippedVersions(t *testing.T) {
	source := &releaseSourceStub{releases: []Release{
		{TagName: "v0.2.4", Name: "latest"},
		{TagName: "v0.2.2", Name: "next"},
		{TagName: "v0.2.3", Name: "middle"},
	}}
	checker := NewChecker("v0.2.1", source)

	info, err := checker.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Managed || !info.HasUpdate || info.NextVersion != "v0.2.4" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.SkippedVersions) != 2 || info.SkippedVersions[0] != "v0.2.2" || info.SkippedVersions[1] != "v0.2.3" {
		t.Fatalf("skipped = %v", info.SkippedVersions)
	}
	if info.Release == nil || info.Release.Name != "latest" {
		t.Fatalf("release = %+v", info.Release)
	}
	if len(info.RecentReleases) != 4 || info.RecentReleases[0].TagName != "v0.2.4" || info.RecentReleases[3].TagName != "v0.2.1" {
		t.Fatalf("recent = %+v", info.RecentReleases)
	}
}

func TestCheckerDisablesDevelopmentBuilds(t *testing.T) {
	source := &releaseSourceStub{releases: []Release{{TagName: "v0.2.2"}}}
	checker := NewChecker("dev", source)

	info, err := checker.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if info.Managed || info.HasUpdate || source.calls != 0 {
		t.Fatalf("info=%+v calls=%d", info, source.calls)
	}
}

// A managed build whose repository has not published any stable release is
// not "up to date": the console needs an explicit count to tell the two
// apart, and drafts/prereleases/non-semver tags must not count.
func TestCheckerReportsPublishedReleaseCount(t *testing.T) {
	empty := NewChecker("v0.1.0", &releaseSourceStub{})
	info, err := empty.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Managed || info.HasUpdate || info.PublishedReleases != 0 {
		t.Fatalf("info=%+v", info)
	}

	mixed := NewChecker("v0.1.0", &releaseSourceStub{releases: []Release{
		{TagName: "v0.1.0"},
		{TagName: "v0.1.1", Draft: true},
		{TagName: "v0.1.2", Prerelease: true},
		{TagName: "nightly"},
		{TagName: "v0.1.0"}, // duplicate tag listing
		{TagName: "0.1.3"},  // parseable without the v prefix
	}})
	counted, err := mixed.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if counted.PublishedReleases != 2 {
		t.Fatalf("published=%d want 2 (info=%+v)", counted.PublishedReleases, counted)
	}
	if !counted.HasUpdate || counted.NextVersion != "v0.1.3" {
		t.Fatalf("info=%+v", counted)
	}
}

func TestCheckerForceCheckDoesNotUseStaleRelease(t *testing.T) {
	source := &releaseSourceStub{releases: []Release{{TagName: "v0.2.2"}}}
	checker := NewChecker("v0.2.1", source)
	if _, err := checker.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	source.err = context.DeadlineExceeded
	if _, err := checker.Check(context.Background(), true); err == nil {
		t.Fatal("forced update check unexpectedly used stale cache")
	}
}
