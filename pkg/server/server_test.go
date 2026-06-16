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
		wantCount   int // expected number of "NFS_Protocols = 4;" occurrences
	}{
		{
			name:        "v4-only adds the directive to a default config",
			input:       string(defaultGaneshaConfigContents),
			enableNFSv3: false,
			wantCount:   1,
		},
		{
			name:        "v3 enabled leaves a default config untouched",
			input:       string(defaultGaneshaConfigContents),
			enableNFSv3: true,
			wantCount:   0,
		},
		{
			name:        "v4-only is idempotent (no duplicate on re-run)",
			input:       "NFS_Core_Param\n{\n\tNFS_Protocols = 4;\n\tMNT_Port = 20048;\n}\n",
			enableNFSv3: false,
			wantCount:   1,
		},
		{
			name:        "toggling back to v3 removes a previously added directive",
			input:       "NFS_Core_Param\n{\n\tNFS_Protocols = 4;\n\tMNT_Port = 20048;\n}\n",
			enableNFSv3: true,
			wantCount:   0,
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
			if got := strings.Count(string(out), "NFS_Protocols = 4;"); got != test.wantCount {
				t.Errorf("NFS_Protocols=4 count = %d, want %d\nconfig:\n%s", got, test.wantCount, out)
			}
			// When disabled, the directive must land inside the NFS_Core_Param block.
			if !test.enableNFSv3 && !strings.Contains(string(out), "NFS_Core_Param") {
				t.Errorf("NFS_Core_Param block missing from result:\n%s", out)
			}
		})
	}
}
