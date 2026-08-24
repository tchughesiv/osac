/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package it

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2/dsl/core"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"google.golang.org/grpc"

	privatev1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/private/v1"
	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
	"github.com/osac-project/osac/fulfillment-service/internal/auth"
	"github.com/osac-project/osac/fulfillment-service/internal/cmd/service/start/mcpserver"
	"github.com/osac-project/osac/fulfillment-service/internal/network"
	"github.com/osac-project/osac/fulfillment-service/internal/uuid"
)

var _ = Describe("MCP server", func() {
	var (
		ctx                context.Context
		hostTypesClient    privatev1.HostTypesClient
		templatesClient    privatev1.ClusterTemplatesClient
		catalogItemsClient privatev1.ClusterCatalogItemsClient
		hostTypeId         string
		templateId         string
		catalogItemId      string
		mcpGrpcConn        *grpc.ClientConn
		mcpHTTPServer      *httptest.Server
		mcpClient          *http.Client
	)

	BeforeEach(func() {
		// Create a context:
		ctx = context.Background()

		// Create the clients (mirrors it_public_clusters_test.go's fixture pattern):
		hostTypesClient = privatev1.NewHostTypesClient(tool.InternalView().AdminConn())
		templatesClient = privatev1.NewClusterTemplatesClient(tool.InternalView().AdminConn())
		catalogItemsClient = privatev1.NewClusterCatalogItemsClient(tool.InternalView().AdminConn())

		// Create a host type for testing:
		hostTypeId = fmt.Sprintf("mcp-host-type-%s", uuid.New())
		_, err := hostTypesClient.Create(ctx, privatev1.HostTypesCreateRequest_builder{
			Object: privatev1.HostType_builder{
				Metadata: privatev1.Metadata_builder{
					Name: fmt.Sprintf("mcp-ht-%s", uuid.New()[24:32]),
				}.Build(),
				Id: hostTypeId,
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			_, err := hostTypesClient.Delete(ctx, privatev1.HostTypesDeleteRequest_builder{
				Id: hostTypeId,
			}.Build())
			Expect(err).ToNot(HaveOccurred())
		})

		// Create a template for testing:
		templateId = fmt.Sprintf("mcp-template-%s", uuid.New())
		_, err = templatesClient.Create(ctx, privatev1.ClusterTemplatesCreateRequest_builder{
			Object: privatev1.ClusterTemplate_builder{
				Metadata: privatev1.Metadata_builder{
					Name: fmt.Sprintf("mcp-tmpl-%s", uuid.New()[24:32]),
				}.Build(),
				Id:          templateId,
				Title:       "MCP test template",
				Description: "Template used by the MCP server integration test.",
				NodeSets: map[string]*privatev1.ClusterTemplateNodeSet{
					"my-node-set": privatev1.ClusterTemplateNodeSet_builder{
						HostType: privatev1.HostTypeReference_builder{Id: hostTypeId}.Build(),
						Size:     3,
					}.Build(),
				},
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			_, err := templatesClient.Delete(ctx, privatev1.ClusterTemplatesDeleteRequest_builder{
				Id: templateId,
			}.Build())
			Expect(err).ToNot(HaveOccurred())
		})

		// Create a published catalog item referencing that template. No tenant is set explicitly — same convention
		// the host type and template fixtures above use — which lands it in the "shared" tenant (the default
		// tenant DetermineDefaultTenant assigns when the creator, here the admin connection, can assign any
		// tenant), making it visible to every tenant, including the regular test user's.
		catalogItemId = fmt.Sprintf("mcp-catalog-item-%s", uuid.New())
		_, err = catalogItemsClient.Create(ctx, privatev1.ClusterCatalogItemsCreateRequest_builder{
			Object: privatev1.ClusterCatalogItem_builder{
				Id: catalogItemId,
				Metadata: privatev1.Metadata_builder{
					Name: fmt.Sprintf("mcp-catalog-item-%s", uuid.New()[24:32]),
				}.Build(),
				Title:       "MCP test catalog item",
				Description: "Catalog item used by the MCP server integration test.",
				Published:   true,
				Template:    privatev1.ClusterTemplateReference_builder{Id: templateId}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			_, err := catalogItemsClient.Delete(ctx, privatev1.ClusterCatalogItemsDeleteRequest_builder{
				Id: catalogItemId,
			}.Build())
			Expect(err).ToNot(HaveOccurred())
		})

		// Build a JWT validator wired to the suite's own Keycloak, mirroring exactly how grpcserver's own
		// interceptor (and mcpserver's own RunE) is configured, so this test exercises the real verification path.
		jwksCache, err := auth.NewJwksCache().
			SetLogger(logger).
			SetCaPool(tool.CaPool()).
			AddIssuers(fmt.Sprintf("https://%s/realms/osac", keycloakAddr)).
			Build()
		Expect(err).ToNot(HaveOccurred())
		jwtValidator, err := auth.NewJwtValidator().
			SetLogger(logger).
			SetJwksCache(jwksCache).
			SetExpirationLeeway(5 * time.Second).
			Build()
		Expect(err).ToNot(HaveOccurred())

		// Dial the downstream gRPC client with no token source: every call carries the caller's own bearer token,
		// forwarded per-call by mcpserver's forwardToken, matching the production wiring in start_mcp_server_cmd.go.
		mcpGrpcConn, err = network.NewGrpcClient().
			SetLogger(logger).
			SetCaPool(tool.CaPool()).
			SetAddress(externalServiceAddr).
			SetUserAgent("fulfillment-mcp-server-it").
			Build()
		Expect(err).ToNot(HaveOccurred())

		// Stand up mcpserver's real HTTP handler — tool registration, Streamable HTTP transport, and bearer-token
		// verification — reusing mcpserver.NewHandler so this test exercises the exact composition the real
		// command serves, not a hand-rolled reconstruction of it.
		handler := mcpserver.NewHandler(mcpserver.ServerDeps{
			CatalogItemsClient: publicv1.NewClusterCatalogItemsClient(mcpGrpcConn),
			ClustersClient:     publicv1.NewClustersClient(mcpGrpcConn),
		}, jwtValidator)
		mcpHTTPServer = httptest.NewServer(handler)
		DeferCleanup(func() {
			mcpHTTPServer.Close()
			Expect(mcpGrpcConn.Close()).ToNot(HaveOccurred())
		})

		// Build an HTTP client that injects the suite's own regular user's bearer token on every request, following
		// it_tool.go's makeHttpClient pattern but pointed at mcpserver's own test listener instead of restgateway's.
		mcpClient = &http.Client{
			Transport: ghttp.RoundTripperFunc(
				func(request *http.Request) (response *http.Response, err error) {
					token, err := tool.UserTokenSource().Token(request.Context())
					if err != nil {
						return nil, err
					}
					request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.Access))
					return http.DefaultTransport.RoundTrip(request)
				},
			),
		}
	})

	It("Drives list, describe, create and get_status over real HTTP and auth, attributed to the calling user", func() {
		// Connect an MCP client to the test server, authenticating as the suite's regular test user:
		transport := &mcp.StreamableClientTransport{
			Endpoint:   mcpHTTPServer.URL,
			HTTPClient: mcpClient,
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "it-mcp-client", Version: "0.0.1"}, nil)
		session, err := client.Connect(ctx, transport, nil)
		Expect(err).ToNot(HaveOccurred())
		defer func() {
			Expect(session.Close()).ToNot(HaveOccurred())
		}()

		// list_catalog_items should surface the seeded catalog item:
		listOutput, err := callMCPTool[mcpserver.ListCatalogItemsOutput](
			ctx, session, "list_catalog_items", mcpserver.ListCatalogItemsInput{
				Filter: fmt.Sprintf("this.id == %q", catalogItemId),
			},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(listOutput.Items).To(HaveLen(1))
		Expect(listOutput.Items[0].ID).To(Equal(catalogItemId))

		// describe_catalog_item should resolve the same item by id:
		describeOutput, err := callMCPTool[mcpserver.DescribeCatalogItemOutput](
			ctx, session, "describe_catalog_item", mcpserver.DescribeCatalogItemInput{ID: catalogItemId},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(describeOutput.ID).To(Equal(catalogItemId))
		Expect(describeOutput.Title).To(Equal("MCP test catalog item"))

		// create_cluster_from_catalog_item should create a real cluster, attributed to the calling user:
		clusterName := fmt.Sprintf("mcp-cluster-%s", uuid.New()[24:32])
		createOutput, err := callMCPTool[mcpserver.CreateClusterFromCatalogItemOutput](
			ctx, session, "create_cluster_from_catalog_item", mcpserver.CreateClusterFromCatalogItemInput{
				Name:        clusterName,
				CatalogItem: catalogItemId,
			},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(createOutput.ID).ToNot(BeEmpty())
		DeferCleanup(func() {
			clustersClient := publicv1.NewClustersClient(tool.ExternalView().UserConn())
			_, err := clustersClient.Delete(ctx, publicv1.ClustersDeleteRequest_builder{
				Id: createOutput.ID,
			}.Build())
			Expect(err).ToNot(HaveOccurred())
		})

		// get_cluster_status should return a real, non-unspecified state. Create returns before real provisioning
		// completes, so this only asserts a valid in-progress state, not READY.
		statusOutput, err := callMCPTool[mcpserver.GetClusterStatusOutput](
			ctx, session, "get_cluster_status", mcpserver.GetClusterStatusInput{ID: createOutput.ID},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(statusOutput.ID).To(Equal(createOutput.ID))
		Expect(statusOutput.State).ToNot(BeEmpty())
		Expect(statusOutput.State).ToNot(Equal("CLUSTER_STATE_UNSPECIFIED"))

		// The concrete proof of token passthrough: fetch the created cluster directly and confirm its
		// metadata.creator/metadata.tenant match the real calling user's identity, not a shared service account.
		usersClient := privatev1.NewUsersClient(tool.InternalView().AdminConn())
		usersResponse, err := usersClient.List(ctx, privatev1.UsersListRequest_builder{
			Filter: new(fmt.Sprintf("this.spec.username == %q", userUsername)),
			Limit:  new(int32(1)),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		Expect(usersResponse.GetSize()).To(Equal(int32(1)))
		expectedUserID := usersResponse.GetItems()[0].GetId()

		clustersClient := publicv1.NewClustersClient(tool.ExternalView().UserConn())
		getResponse, err := clustersClient.Get(ctx, publicv1.ClustersGetRequest_builder{
			Id: createOutput.ID,
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		clusterMetadata := getResponse.GetObject().GetMetadata()
		Expect(clusterMetadata.GetCreator()).To(Equal(expectedUserID))
		Expect(clusterMetadata.GetTenant()).To(Equal(usersGroup))
	})
})

// callMCPTool calls the named MCP tool with the given arguments and unmarshals its structured output into Out. It
// fails the current spec (via Gomega's global fail handler) if the call itself errors or the tool reports IsError,
// so call sites can treat the returned value as valid without an extra error check.
func callMCPTool[Out any](ctx context.Context, session *mcp.ClientSession, name string, arguments any) (Out, error) {
	var output Out
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return output, fmt.Errorf("failed to call tool '%s': %w", name, err)
	}
	if result.IsError {
		return output, fmt.Errorf("tool '%s' returned an error result: %+v", name, result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return output, fmt.Errorf("failed to marshal structured content from tool '%s': %w", name, err)
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, fmt.Errorf("failed to unmarshal structured content from tool '%s': %w", name, err)
	}
	return output, nil
}
