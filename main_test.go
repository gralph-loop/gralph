package main

import "testing"

// Regression for issue #3: the profile path must be accepted both before and
// after the flags.
func TestParseRunArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		path    string
		maxIter int
		wantErr bool
	}{
		{"path first", []string{"profile.yaml", "--max-iterations", "3"}, "profile.yaml", 3, false},
		{"flags first", []string{"--max-iterations", "3", "profile.yaml"}, "profile.yaml", 3, false},
		{"path only", []string{"profile.yaml"}, "profile.yaml", 0, false},
		{"no path", []string{"--max-iterations", "3"}, "", 0, true},
		{"empty", nil, "", 0, true},
		{"two paths", []string{"a.yaml", "b.yaml"}, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, maxIter, err := parseRunArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got path=%q maxIter=%d", path, maxIter)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if path != tc.path || maxIter != tc.maxIter {
				t.Fatalf("got path=%q maxIter=%d, want path=%q maxIter=%d",
					path, maxIter, tc.path, tc.maxIter)
			}
		})
	}
}
