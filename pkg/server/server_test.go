/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetNFSProtocols(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		enableNFSv3 bool
		wantV4Only  bool
	}{
		{
			name:        "v4-only adds directives to a default config",
			input:       string(defaultGaneshaConfigContents),
			enableNFSv3: false,
			wantV4Only:  true,
		},
		{
			name:        "v3 enabled leaves a default config untouched",
			input:       string(defaultGaneshaConfigContents),
			enableNFSv3: true,
			wantV4Only:  false,
		},
		{
			name: "v4-only is idempotent (no duplicate on re-run)",
			input: "NFS_Core_Param\n{\n\tNFS_Protocols = 4;\n\tEnable_UDP = false;\n" +
				"\tEnable_RQUOTA = false;\n\tMNT_Port = 20048;\n}\n",
			enableNFSv3: false,
			wantV4Only:  true,
		},
		{
			name: "toggling back to v3 removes previously added directives",
			input: "NFS_Core_Param\n{\n\tNFS_Protocols = 4;\n\tEnable_UDP = false;\n" +
				"\tEnable_RQUOTA = false;\n\tMNT_Port = 20048;\n}\n",
			enableNFSv3: true,
			wantV4Only:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vfs.conf")
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}

			if err := setNFSProtocols(path, test.enableNFSv3); err != nil {
				t.Fatalf("setNFSProtocols: %v", err)
			}

			out, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading result: %v", err)
			}
			outStr := string(out)
			for _, line := range []string{
				"NFS_Protocols = 4;",
				"Enable_UDP = false;",
				"Enable_RQUOTA = false;",
			} {
				got := strings.Count(outStr, line)
				want := 0
				if test.wantV4Only {
					want = 1
				}
				if got != want {
					t.Errorf("%q count = %d, want %d\nconfig:\n%s", line, got, want, outStr)
				}
			}
			if test.wantV4Only && !strings.Contains(outStr, "NFS_Core_Param") {
				t.Errorf("NFS_Core_Param block missing from result:\n%s", outStr)
			}
		})
	}
}
