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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
)

// mockClustersClient is a minimal mock that intercepts Create and Get calls, following the convention in
// internal/cmd/cli/create/cluster/create_cluster_cmd_test.go.
type mockClustersClient struct {
	publicv1.ClustersClient
	createFunc func(ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption) (*publicv1.ClustersCreateResponse, error)
	getFunc    func(ctx context.Context, req *publicv1.ClustersGetRequest, opts ...grpc.CallOption) (*publicv1.ClustersGetResponse, error)
}

func (m *mockClustersClient) Create(
	ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption,
) (*publicv1.ClustersCreateResponse, error) {
	return m.createFunc(ctx, req, opts...)
}

func (m *mockClustersClient) Get(
	ctx context.Context, req *publicv1.ClustersGetRequest, opts ...grpc.CallOption,
) (*publicv1.ClustersGetResponse, error) {
	return m.getFunc(ctx, req, opts...)
}

var _ = Describe("handleCreateClusterFromCatalogItem", func() {
	It("Builds the cluster from the catalog item reference and forwards the caller's token", func() {
		var capturedToken string
		var capturedObject *publicv1.Cluster
		client := &mockClustersClient{
			createFunc: func(
				ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersCreateResponse, error) {
				capturedToken = forwardedToken(ctx)
				capturedObject = req.GetObject()
				return publicv1.ClustersCreateResponse_builder{
					Object: publicv1.Cluster_builder{
						Id: "cluster-1",
						Status: publicv1.ClusterStatus_builder{
							State: publicv1.ClusterState_CLUSTER_STATE_PROGRESSING,
						}.Build(),
					}.Build(),
				}.Build(), nil
			},
		}

		handler := handleCreateClusterFromCatalogItem(client)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateClusterFromCatalogItemInput{
			Name:        "my-cluster",
			CatalogItem: "ocp-example",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output).To(Equal(CreateClusterFromCatalogItemOutput{
			ID:    "cluster-1",
			State: "CLUSTER_STATE_PROGRESSING",
		}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
		Expect(capturedObject.GetMetadata().GetName()).To(Equal("my-cluster"))
		Expect(capturedObject.GetSpec().GetCatalogItem().GetId()).To(Equal("ocp-example"))
		// metadata.tenant/creator are intentionally left unset here — the server assigns both from the
		// caller's own forwarded identity, which is the attribution behavior this tool demonstrates.
		Expect(capturedObject.GetMetadata().GetTenant()).To(BeEmpty())
	})

	It("Applies --set-style field overrides onto the spec", func() {
		var capturedObject *publicv1.Cluster
		client := &mockClustersClient{
			createFunc: func(
				ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersCreateResponse, error) {
				capturedObject = req.GetObject()
				return publicv1.ClustersCreateResponse_builder{
					Object: publicv1.Cluster_builder{Id: "cluster-1"}.Build(),
				}.Build(), nil
			},
		}

		handler := handleCreateClusterFromCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateClusterFromCatalogItemInput{
			Name:        "my-cluster",
			CatalogItem: "ocp-example",
			Set:         []string{"ssh_public_key=ssh-ed25519 AAAA"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(capturedObject.GetSpec().GetSshPublicKey()).To(Equal("ssh-ed25519 AAAA"))
	})

	It("Returns an error when a field override is malformed", func() {
		client := &mockClustersClient{
			createFunc: func(
				ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersCreateResponse, error) {
				return nil, errors.New("Create should not be called")
			},
		}

		handler := handleCreateClusterFromCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateClusterFromCatalogItemInput{
			Name:        "my-cluster",
			CatalogItem: "ocp-example",
			Set:         []string{"missing-equals-sign"},
		})
		Expect(err).To(HaveOccurred())
	})

	It("Propagates a Create error", func() {
		client := &mockClustersClient{
			createFunc: func(
				ctx context.Context, req *publicv1.ClustersCreateRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersCreateResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleCreateClusterFromCatalogItem(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateClusterFromCatalogItemInput{
			Name:        "my-cluster",
			CatalogItem: "ocp-example",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("boom"))
	})
})

var _ = Describe("handleGetClusterStatus", func() {
	It("Maps the cluster's status and forwards the caller's token", func() {
		var capturedToken string
		client := &mockClustersClient{
			getFunc: func(
				ctx context.Context, req *publicv1.ClustersGetRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersGetResponse, error) {
				capturedToken = forwardedToken(ctx)
				Expect(req.GetId()).To(Equal("cluster-1"))
				return publicv1.ClustersGetResponse_builder{
					Object: publicv1.Cluster_builder{
						Id: "cluster-1",
						Status: publicv1.ClusterStatus_builder{
							State:      publicv1.ClusterState_CLUSTER_STATE_READY,
							ApiUrl:     "https://api.example.com",
							ConsoleUrl: "https://console.example.com",
							Conditions: []*publicv1.ClusterCondition{
								publicv1.ClusterCondition_builder{
									Type:    publicv1.ClusterConditionType_CLUSTER_CONDITION_TYPE_READY,
									Status:  publicv1.ConditionStatus_CONDITION_STATUS_TRUE,
									Message: proto.String("The cluster is ready to use"),
								}.Build(),
							},
						}.Build(),
					}.Build(),
				}.Build(), nil
			},
		}

		handler := handleGetClusterStatus(client)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetClusterStatusInput{
			ID: "cluster-1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output).To(Equal(GetClusterStatusOutput{
			ID:         "cluster-1",
			State:      "CLUSTER_STATE_READY",
			APIURL:     "https://api.example.com",
			ConsoleURL: "https://console.example.com",
			Conditions: []ConditionSummary{{
				Type:    "CLUSTER_CONDITION_TYPE_READY",
				Status:  "CONDITION_STATUS_TRUE",
				Message: "The cluster is ready to use",
			}},
		}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Propagates a Get error", func() {
		client := &mockClustersClient{
			getFunc: func(
				ctx context.Context, req *publicv1.ClustersGetRequest, opts ...grpc.CallOption,
			) (*publicv1.ClustersGetResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleGetClusterStatus(client)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), GetClusterStatusInput{
			ID: "missing",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("boom"))
	})
})
