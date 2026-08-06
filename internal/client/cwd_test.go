package client

import "testing"

func TestRelCwd(t *testing.T) {
	const root = "/workspace/dev/my/csb"

	tests := []struct {
		name    string
		root    string
		dir     string
		want    string
		wantErr bool
	}{
		{name: "root itself", root: root, dir: root, want: "."},
		{name: "root with trailing slash", root: root + "/", dir: root, want: "."},
		{name: "subdirectory", root: root, dir: root + "/internal/broker", want: "internal/broker"},
		{name: "uncleaned path", root: root, dir: root + "/internal/../cmd", want: "cmd"},
		{name: "sibling with shared prefix", root: root, dir: root + "-other", wantErr: true},
		{name: "outside workspace", root: root, dir: "/etc", wantErr: true},
		{name: "parent of workspace", root: root, dir: "/workspace/dev", wantErr: true},
		{name: "no workspace", root: "", dir: "/workspace", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RelCwd(tc.root, tc.dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RelCwd(%q, %q) = %q, want error", tc.root, tc.dir, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RelCwd(%q, %q): %v", tc.root, tc.dir, err)
			}
			if got != tc.want {
				t.Errorf("RelCwd(%q, %q) = %q, want %q", tc.root, tc.dir, got, tc.want)
			}
		})
	}
}
