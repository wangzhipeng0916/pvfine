package main

import "testing"

func TestIsReleaseBuild(t *testing.T) {
	originalVersion, originalRepository := appVersion, githubRepository
	t.Cleanup(func() {
		appVersion, githubRepository = originalVersion, originalRepository
	})

	cases := []struct {
		version      string
		repository   string
		wantReleased bool
	}{
		{version: "0.0.0", repository: "dof-dev/pvfine", wantReleased: false},
		{version: "dev", repository: "dof-dev/pvfine", wantReleased: false},
		{version: "", repository: "dof-dev/pvfine", wantReleased: false},
		{version: "0.16.0", repository: "", wantReleased: false},
		{version: "0.16.0", repository: "dof-dev/pvfine", wantReleased: true},
		{version: "v0.16.0", repository: "dof-dev/pvfine", wantReleased: true},
	}
	for _, tc := range cases {
		appVersion, githubRepository = tc.version, tc.repository
		if got := isReleaseBuild(); got != tc.wantReleased {
			t.Fatalf("isReleaseBuild() with appVersion=%q repository=%q = %v, want %v",
				tc.version, tc.repository, got, tc.wantReleased)
		}
	}
}
