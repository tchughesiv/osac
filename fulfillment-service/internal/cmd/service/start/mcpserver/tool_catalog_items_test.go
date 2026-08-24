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
	"errors"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
)

// mockClusterCatalogItemsClient is a minimal mock that intercepts List calls, following the convention in
// internal/cmd/cli/create/cluster/create_cluster_cmd_test.go.
type mockClusterCatalogItemsClient struct {
	publicv1.ClusterCatalogItemsClient
	listFunc func(ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption) (*publicv1.ClusterCatalogItemsListResponse, error)
}

func (m *mockClusterCatalogItemsClient) List(
	ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
) (*publicv1.ClusterCatalogItemsListResponse, error) {
	return m.listFunc(ctx, req, opts...)
}

// requestWithToken builds a *mcp.CallToolRequest carrying the given raw bearer token in TokenInfo, matching what
// RequireBearerToken's middleware attaches for a real authenticated call.
func requestWithToken(rawToken string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Extra: &mcp.RequestExtra{
			TokenInfo: &sdkauth.TokenInfo{
				Extra: map[string]any{
					rawTokenExtraKey: rawToken,
				},
			},
		},
	}
}

// forwardedToken extracts the bearer token forwarded to the outgoing gRPC context, or "" if none was forwarded.
func forwardedToken(ctx context.Context) string {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

var _ = Describe("handleListCatalogItems", func() {
	It("Maps the response items and forwards the caller's token", func() {
		var capturedToken string
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				capturedToken = forwardedToken(ctx)
				return publicv1.ClusterCatalogItemsListResponse_builder{
					Items: []*publicv1.ClusterCatalogItem{
						publicv1.ClusterCatalogItem_builder{
							Id:          "item-1",
							Title:       "Example item",
							Description: "An example catalog item",
						}.Build(),
					},
				}.Build(), nil
			},
		}

		handler := handleListCatalogItems(client)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListCatalogItemsInput{})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Items).To(HaveLen(1))
		Expect(output.Items[0]).To(Equal(CatalogItemSummary{
			ID:          "item-1",
			Title:       "Example item",
			Description: "An example catalog item",
		}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Passes a non-empty filter through to the List request", func() {
		var capturedFilter string
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				capturedFilter = req.GetFilter()
				return publicv1.ClusterCatalogItemsListResponse_builder{}.Build(), nil
			},
		}

		handler := handleListCatalogItems(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListCatalogItemsInput{
			Filter: "this.published == true",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(capturedFilter).To(Equal("this.published == true"))
	})

	It("Propagates a List error", func() {
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleListCatalogItems(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListCatalogItemsInput{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("boom"))
	})
})

var _ = Describe("handleDescribeCatalogItem", func() {
	It("Maps the resolved item's field definitions and forwards the caller's token", func() {
		defaultValue, err := structpb.NewValue(3)
		Expect(err).ToNot(HaveOccurred())

		var capturedToken string
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				capturedToken = forwardedToken(ctx)
				return publicv1.ClusterCatalogItemsListResponse_builder{
					Items: []*publicv1.ClusterCatalogItem{
						publicv1.ClusterCatalogItem_builder{
							Id:          "item-1",
							Title:       "Example item",
							Description: "An example catalog item",
							FieldDefinitions: []*publicv1.FieldDefinition{
								publicv1.FieldDefinition_builder{
									Path:        "spec.replicas",
									DisplayName: "Replicas",
									Editable:    true,
									Default:     defaultValue,
								}.Build(),
							},
						}.Build(),
					},
				}.Build(), nil
			},
		}

		handler := handleDescribeCatalogItem(client)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DescribeCatalogItemInput{
			ID: "item-1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.ID).To(Equal("item-1"))
		Expect(output.Title).To(Equal("Example item"))
		Expect(output.FieldDefinitions).To(Equal([]FieldDefinitionSummary{{
			Path:         "spec.replicas",
			DisplayName:  "Replicas",
			Editable:     true,
			DefaultValue: "3",
		}}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Returns an error when no catalog item matches", func() {
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				return publicv1.ClusterCatalogItemsListResponse_builder{}.Build(), nil
			},
		}

		handler := handleDescribeCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DescribeCatalogItemInput{
			ID: "missing",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})

	It("Returns an error when the reference is ambiguous", func() {
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				return publicv1.ClusterCatalogItemsListResponse_builder{
					Items: []*publicv1.ClusterCatalogItem{
						publicv1.ClusterCatalogItem_builder{Id: "item-1"}.Build(),
						publicv1.ClusterCatalogItem_builder{Id: "item-2"}.Build(),
					},
				}.Build(), nil
			},
		}

		handler := handleDescribeCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DescribeCatalogItemInput{
			ID: "ambiguous",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ambiguous"))
	})

	It("Propagates a List error", func() {
		client := &mockClusterCatalogItemsClient{
			listFunc: func(
				ctx context.Context, req *publicv1.ClusterCatalogItemsListRequest, opts ...grpc.CallOption,
			) (*publicv1.ClusterCatalogItemsListResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleDescribeCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DescribeCatalogItemInput{
			ID: "item-1",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("boom"))
	})
})

var _ = Describe("defaultValueString", func() {
	It("Returns an empty string when the field definition has no default", func() {
		definition := publicv1.FieldDefinition_builder{Path: "spec.replicas"}.Build()
		Expect(defaultValueString(definition)).To(Equal(""))
	})

	It("Stringifies a scalar default value", func() {
		value, err := structpb.NewValue("gp3")
		Expect(err).ToNot(HaveOccurred())
		definition := publicv1.FieldDefinition_builder{Path: "spec.storageClass", Default: value}.Build()
		Expect(defaultValueString(definition)).To(Equal("gp3"))
	})
})
