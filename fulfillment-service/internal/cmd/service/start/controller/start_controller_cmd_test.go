/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package controller

import "testing"

func TestKeycloakRealmName(t *testing.T) {
	tests := []struct {
		name    string
		issuer  string
		want    string
		wantErr bool
	}{
		{
			name:   "external realm",
			issuer: "https://sso.example.com/realms/osac-demo",
			want:   "osac-demo",
		},
		{
			name:   "legacy context path",
			issuer: "https://keycloak.example.com/auth/realms/osac",
			want:   "osac",
		},
		{
			name:    "missing realm path",
			issuer:  "https://keycloak.example.com",
			wantErr: true,
		},
		{
			name:    "invalid URL",
			issuer:  "://not-a-url",
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := keycloakRealmName(test.issuer)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != test.want {
				t.Fatalf("expected realm %q, got %q", test.want, got)
			}
		})
	}
}
