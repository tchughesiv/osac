/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/metadata"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osac-project/osac/fulfillment-service/internal/auth"
)

var _ = Describe("Cmd", func() {
	It("Has the expected use string", func() {
		cmd := Cmd()
		Expect(cmd.Use).To(Equal("mcp-server [FLAG...]"))
	})

	It("Has the HTTP listener flags", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("http-listener-address")).ToNot(BeNil())
	})

	It("Has the CORS flags", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("http-cors-allowed-origins")).ToNot(BeNil())
	})

	It("Has the gRPC client flags", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("grpc-server-address")).ToNot(BeNil())
	})

	It("Has a --grpc-authn-trusted-token-issuers flag", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("grpc-authn-trusted-token-issuers")).ToNot(BeNil())
	})

	It("Has a --ca-file flag", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("ca-file")).ToNot(BeNil())
	})

	It("Has an --oauth-authorization-server flag", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("oauth-authorization-server")).ToNot(BeNil())
	})

	It("Has an --oauth-resource-url flag", func() {
		cmd := Cmd()
		Expect(cmd.Flags().Lookup("oauth-resource-url")).ToNot(BeNil())
	})

	It("Accepts no arguments", func() {
		cmd := Cmd()
		Expect(cmd.Args).ToNot(BeNil())
	})
})

var _ = Describe("newTokenVerifier", func() {
	var ctrl *gomock.Controller

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		DeferCleanup(ctrl.Finish)
	})

	It("Maps a validated token to TokenInfo carrying the raw token", func() {
		expiration := time.Now().Add(time.Hour)
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": "alice",
			"exp": float64(expiration.Unix()),
		})
		validator := auth.NewMockJwtValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), "raw-bearer-value").Return(token, nil)

		verifier := newTokenVerifier(validator)
		info, err := verifier(context.Background(), "raw-bearer-value", nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.UserID).To(Equal("alice"))
		Expect(info.Expiration.Unix()).To(Equal(expiration.Unix()))
		Expect(info.Extra[rawTokenExtraKey]).To(Equal("raw-bearer-value"))
	})

	It("Wraps a validation failure with sdkauth.ErrInvalidToken", func() {
		validator := auth.NewMockJwtValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(nil, errors.New("token signature is not valid"))

		verifier := newTokenVerifier(validator)
		_, err := verifier(context.Background(), "bad-token", nil)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, sdkauth.ErrInvalidToken)).To(BeTrue())
	})

	It("Rejects a validated token with no subject claim", func() {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"exp": float64(time.Now().Add(time.Hour).Unix()),
		})
		validator := auth.NewMockJwtValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(token, nil)

		verifier := newTokenVerifier(validator)
		_, err := verifier(context.Background(), "raw-bearer-value", nil)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, sdkauth.ErrInvalidToken)).To(BeTrue())
	})

	It("Rejects a validated token with no expiration claim", func() {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": "alice",
		})
		validator := auth.NewMockJwtValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), gomock.Any()).Return(token, nil)

		verifier := newTokenVerifier(validator)
		_, err := verifier(context.Background(), "raw-bearer-value", nil)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, sdkauth.ErrInvalidToken)).To(BeTrue())
	})
})

var _ = Describe("forwardToken", func() {
	It("Adds the forwarded bearer token to the outgoing gRPC metadata", func() {
		ctx := context.Background()
		req := &mcp.CallToolRequest{
			Extra: &mcp.RequestExtra{
				TokenInfo: &sdkauth.TokenInfo{
					Extra: map[string]any{
						rawTokenExtraKey: "raw-bearer-value",
					},
				},
			},
		}

		result := forwardToken(ctx, req)
		md, ok := metadata.FromOutgoingContext(result)
		Expect(ok).To(BeTrue())
		Expect(md.Get("authorization")).To(Equal([]string{"Bearer raw-bearer-value"}))
	})

	It("Returns the context unchanged when the request has no Extra", func() {
		ctx := context.Background()
		result := forwardToken(ctx, &mcp.CallToolRequest{})
		Expect(result).To(Equal(ctx))
	})

	It("Returns the context unchanged when Extra has no TokenInfo", func() {
		ctx := context.Background()
		req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{}}
		result := forwardToken(ctx, req)
		Expect(result).To(Equal(ctx))
	})

	It("Returns the context unchanged when TokenInfo has no raw token", func() {
		ctx := context.Background()
		req := &mcp.CallToolRequest{
			Extra: &mcp.RequestExtra{
				TokenInfo: &sdkauth.TokenInfo{},
			},
		}
		result := forwardToken(ctx, req)
		Expect(result).To(Equal(ctx))
	})
})

var _ = Describe("NewHandler", func() {
	var ctrl *gomock.Controller

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		DeferCleanup(ctrl.Finish)
	})

	It("Rejects an unauthenticated request with no resource_metadata hint when OAuth discovery is unconfigured", func() {
		validator := auth.NewMockJwtValidator(ctrl)
		handler, err := NewHandler(ServerDeps{}, validator, "", "")
		Expect(err).ToNot(HaveOccurred())

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		handler.ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusUnauthorized))
		Expect(recorder.Header().Get("WWW-Authenticate")).ToNot(ContainSubstring("resource_metadata"))
	})

	It("Rejects configuration with only one of the two OAuth discovery flags set", func() {
		validator := auth.NewMockJwtValidator(ctrl)
		_, err := NewHandler(ServerDeps{}, validator, "https://keycloak.example.com/realms/osac", "")
		Expect(err).To(HaveOccurred())

		_, err = NewHandler(ServerDeps{}, validator, "", "https://mcp.example.com")
		Expect(err).To(HaveOccurred())
	})

	It("Adds a resource_metadata hint to 401s and serves the metadata document, unauthenticated, when both flags are set", func() {
		validator := auth.NewMockJwtValidator(ctrl)
		handler, err := NewHandler(
			ServerDeps{}, validator, "https://keycloak.example.com/realms/osac", "https://mcp.example.com",
		)
		Expect(err).ToNot(HaveOccurred())

		// The main endpoint's 401 now carries a discovery hint:
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		handler.ServeHTTP(recorder, request)
		Expect(recorder.Code).To(Equal(http.StatusUnauthorized))
		Expect(recorder.Header().Get("WWW-Authenticate")).To(ContainSubstring(
			`resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
		))

		// The metadata document itself is served without requiring a bearer token:
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
		handler.ServeHTTP(recorder, request)
		Expect(recorder.Code).To(Equal(http.StatusOK))
		var metadata struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		Expect(json.Unmarshal(recorder.Body.Bytes(), &metadata)).ToNot(HaveOccurred())
		Expect(metadata.Resource).To(Equal("https://mcp.example.com"))
		Expect(metadata.AuthorizationServers).To(Equal([]string{"https://keycloak.example.com/realms/osac"}))
	})

	It("Trims a trailing slash from the resource URL before using it in the hint and metadata document", func() {
		validator := auth.NewMockJwtValidator(ctrl)
		handler, err := NewHandler(
			ServerDeps{}, validator, "https://keycloak.example.com/realms/osac", "https://mcp.example.com/",
		)
		Expect(err).ToNot(HaveOccurred())

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		handler.ServeHTTP(recorder, request)
		Expect(recorder.Header().Get("WWW-Authenticate")).To(ContainSubstring(
			`resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
		))

		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
		handler.ServeHTTP(recorder, request)
		var metadata struct {
			Resource string `json:"resource"`
		}
		Expect(json.Unmarshal(recorder.Body.Bytes(), &metadata)).ToNot(HaveOccurred())
		Expect(metadata.Resource).To(Equal("https://mcp.example.com"))
	})

	It("Still routes an authenticated request through to the streamable transport when both flags are set", func() {
		expiration := time.Now().Add(time.Hour)
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": "alice",
			"exp": float64(expiration.Unix()),
		})
		validator := auth.NewMockJwtValidator(ctrl)
		validator.EXPECT().Validate(gomock.Any(), "valid-token").Return(token, nil)
		handler, err := NewHandler(
			ServerDeps{}, validator, "https://keycloak.example.com/realms/osac", "https://mcp.example.com",
		)
		Expect(err).ToNot(HaveOccurred())

		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer valid-token")
		handler.ServeHTTP(recorder, request)

		// A valid token clears bearer-token verification, so the request reaches the streamable handler and fails
		// for a reason specific to that layer (an empty/non-JSON-RPC body), not for lack of authentication.
		Expect(recorder.Code).ToNot(Equal(http.StatusUnauthorized))
	})
})
