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

	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

type mockComputeInstanceCatalogItemsClient struct {
	publicv1.ComputeInstanceCatalogItemsClient
	listFunc func(context.Context, *publicv1.ComputeInstanceCatalogItemsListRequest, ...grpc.CallOption) (*publicv1.ComputeInstanceCatalogItemsListResponse, error)
	getFunc  func(context.Context, *publicv1.ComputeInstanceCatalogItemsGetRequest, ...grpc.CallOption) (*publicv1.ComputeInstanceCatalogItemsGetResponse, error)
}

func (m *mockComputeInstanceCatalogItemsClient) List(
	ctx context.Context, request *publicv1.ComputeInstanceCatalogItemsListRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstanceCatalogItemsListResponse, error) {
	return m.listFunc(ctx, request, options...)
}

func (m *mockComputeInstanceCatalogItemsClient) Get(
	ctx context.Context, request *publicv1.ComputeInstanceCatalogItemsGetRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstanceCatalogItemsGetResponse, error) {
	return m.getFunc(ctx, request, options...)
}

type mockComputeInstancesClient struct {
	publicv1.ComputeInstancesClient
	listFunc   func(context.Context, *publicv1.ComputeInstancesListRequest, ...grpc.CallOption) (*publicv1.ComputeInstancesListResponse, error)
	getFunc    func(context.Context, *publicv1.ComputeInstancesGetRequest, ...grpc.CallOption) (*publicv1.ComputeInstancesGetResponse, error)
	createFunc func(context.Context, *publicv1.ComputeInstancesCreateRequest, ...grpc.CallOption) (*publicv1.ComputeInstancesCreateResponse, error)
	deleteFunc func(context.Context, *publicv1.ComputeInstancesDeleteRequest, ...grpc.CallOption) (*publicv1.ComputeInstancesDeleteResponse, error)
}

func (m *mockComputeInstancesClient) List(
	ctx context.Context, request *publicv1.ComputeInstancesListRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstancesListResponse, error) {
	return m.listFunc(ctx, request, options...)
}

func (m *mockComputeInstancesClient) Get(
	ctx context.Context, request *publicv1.ComputeInstancesGetRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstancesGetResponse, error) {
	return m.getFunc(ctx, request, options...)
}

func (m *mockComputeInstancesClient) Create(
	ctx context.Context, request *publicv1.ComputeInstancesCreateRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstancesCreateResponse, error) {
	return m.createFunc(ctx, request, options...)
}

func (m *mockComputeInstancesClient) Delete(
	ctx context.Context, request *publicv1.ComputeInstancesDeleteRequest, options ...grpc.CallOption,
) (*publicv1.ComputeInstancesDeleteResponse, error) {
	return m.deleteFunc(ctx, request, options...)
}

func requestWithToken(rawToken string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Extra: &mcp.RequestExtra{
			TokenInfo: &sdkauth.TokenInfo{
				Extra: map[string]any{rawTokenExtraKey: rawToken},
			},
		},
	}
}

func forwardedToken(ctx context.Context) string {
	metadata, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ""
	}
	values := metadata.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

var _ = Describe("handleListResources", func() {
	It("Lists compute instance catalog items and forwards the caller token", func() {
		var capturedToken string
		var capturedOffset, capturedLimit int32
		catalogItems := &mockComputeInstanceCatalogItemsClient{
			listFunc: func(
				ctx context.Context, request *publicv1.ComputeInstanceCatalogItemsListRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstanceCatalogItemsListResponse, error) {
				capturedToken = forwardedToken(ctx)
				capturedOffset = request.GetOffset()
				capturedLimit = request.GetLimit()
				return publicv1.ComputeInstanceCatalogItemsListResponse_builder{
					Size:  1,
					Total: 4,
					Items: []*publicv1.ComputeInstanceCatalogItem{
						publicv1.ComputeInstanceCatalogItem_builder{
							Id:          "catalog-item-1",
							Title:       "Small Fedora VM",
							Description: "A small virtual machine",
						}.Build(),
					},
				}.Build(), nil
			},
		}

		handler := handleListResources(catalogItems, nil)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListResourcesInput{
			ResourceType: ResourceTypeComputeInstanceCatalogItem,
			Offset:       2,
			PageSize:     20,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Items).To(ConsistOf(ResourceSummary{
			ID:          "catalog-item-1",
			Title:       "Small Fedora VM",
			Description: "A small virtual machine",
		}))
		Expect(output.Offset).To(Equal(int32(2)))
		Expect(output.Size).To(Equal(int32(1)))
		Expect(output.Total).To(Equal(int32(4)))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
		Expect(capturedOffset).To(Equal(int32(2)))
		Expect(capturedLimit).To(Equal(int32(20)))
	})

	It("Lists compute instances and forwards an optional filter", func() {
		var capturedToken string
		var capturedFilter string
		var capturedLimit int32
		instances := &mockComputeInstancesClient{
			listFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesListRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesListResponse, error) {
				capturedToken = forwardedToken(ctx)
				capturedFilter = request.GetFilter()
				capturedLimit = request.GetLimit()
				return publicv1.ComputeInstancesListResponse_builder{
					Items: []*publicv1.ComputeInstance{
						publicv1.ComputeInstance_builder{
							Id:       "instance-1",
							Metadata: publicv1.Metadata_builder{Name: "demo-vm"}.Build(),
							Status: publicv1.ComputeInstanceStatus_builder{
								State: publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_RUNNING,
							}.Build(),
						}.Build(),
					},
				}.Build(), nil
			},
		}

		handler := handleListResources(nil, instances)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListResourcesInput{
			ResourceType: ResourceTypeComputeInstance,
			Filter:       "this.metadata.name.startsWith(\"demo\")",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Items).To(ConsistOf(ResourceSummary{
			ID:    "instance-1",
			Name:  "demo-vm",
			State: "COMPUTE_INSTANCE_STATE_RUNNING",
		}))
		Expect(capturedFilter).To(Equal("this.metadata.name.startsWith(\"demo\")"))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
		Expect(capturedLimit).To(Equal(int32(50)))
	})

	It("Rejects an unsupported resource type without calling a downstream client", func() {
		handler := handleListResources(nil, nil)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListResourcesInput{
			ResourceType: "cluster",
		})
		Expect(err).To(MatchError(ContainSubstring("unsupported resource type")))
	})

	It("Rejects an invalid page before calling a downstream client", func() {
		handler := handleListResources(nil, nil)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListResourcesInput{
			ResourceType: ResourceTypeComputeInstance,
			PageSize:     101,
		})
		Expect(err).To(MatchError(ContainSubstring("page_size")))
	})

	It("Propagates a catalog item list error", func() {
		catalogItems := &mockComputeInstanceCatalogItemsClient{
			listFunc: func(
				ctx context.Context, request *publicv1.ComputeInstanceCatalogItemsListRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstanceCatalogItemsListResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleListResources(catalogItems, nil)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), ListResourcesInput{
			ResourceType: ResourceTypeComputeInstanceCatalogItem,
		})
		Expect(err).To(MatchError(ContainSubstring("boom")))
	})
})

var _ = Describe("handleGetResource", func() {
	It("Gets a compute instance catalog item and returns its API representation", func() {
		var capturedToken string
		catalogItems := &mockComputeInstanceCatalogItemsClient{
			getFunc: func(
				ctx context.Context, request *publicv1.ComputeInstanceCatalogItemsGetRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstanceCatalogItemsGetResponse, error) {
				capturedToken = forwardedToken(ctx)
				Expect(request.GetId()).To(Equal("catalog-item-1"))
				return publicv1.ComputeInstanceCatalogItemsGetResponse_builder{
					Object: publicv1.ComputeInstanceCatalogItem_builder{
						Id:    "catalog-item-1",
						Title: "Small Fedora VM",
					}.Build(),
				}.Build(), nil
			},
		}

		handler := handleGetResource(catalogItems, nil)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetResourceInput{
			ResourceType: ResourceTypeComputeInstanceCatalogItem,
			ID:           "catalog-item-1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Resource).To(HaveKeyWithValue("id", "catalog-item-1"))
		Expect(output.Resource).To(HaveKeyWithValue("title", "Small Fedora VM"))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Gets a compute instance and returns its API representation", func() {
		var capturedToken string
		instances := &mockComputeInstancesClient{
			getFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesGetRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesGetResponse, error) {
				capturedToken = forwardedToken(ctx)
				Expect(request.GetId()).To(Equal("instance-1"))
				return publicv1.ComputeInstancesGetResponse_builder{
					Object: publicv1.ComputeInstance_builder{
						Id:       "instance-1",
						Metadata: publicv1.Metadata_builder{Name: "demo-vm"}.Build(),
					}.Build(),
				}.Build(), nil
			},
		}

		handler := handleGetResource(nil, instances)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetResourceInput{
			ResourceType: ResourceTypeComputeInstance,
			ID:           "instance-1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Resource).To(HaveKeyWithValue("id", "instance-1"))
		metadata, ok := output.Resource["metadata"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(metadata).To(HaveKeyWithValue("name", "demo-vm"))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Rejects an unsupported resource type", func() {
		handler := handleGetResource(nil, nil)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetResourceInput{
			ResourceType: "cluster",
			ID:           "cluster-1",
		})
		Expect(err).To(MatchError(ContainSubstring("unsupported resource type")))
	})

	It("Propagates a compute instance Get error", func() {
		instances := &mockComputeInstancesClient{
			getFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesGetRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesGetResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleGetResource(nil, instances)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetResourceInput{
			ResourceType: ResourceTypeComputeInstance,
			ID:           "instance-1",
		})
		Expect(err).To(MatchError(ContainSubstring("boom")))
	})
})

var _ = Describe("messageToMap", func() {
	It("Returns an empty object for a nil message", func() {
		output, err := messageToMap(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(output).To(Equal(map[string]any{}))
	})
})
