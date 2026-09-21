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

	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

var _ = Describe("handleCreateComputeInstance", func() {
	It("Builds a compute instance from the catalog item and forwards the caller token", func() {
		var capturedToken string
		var capturedObject *publicv1.ComputeInstance
		instances := &mockComputeInstancesClient{
			createFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesCreateRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesCreateResponse, error) {
				capturedToken = forwardedToken(ctx)
				capturedObject = request.GetObject()
				return publicv1.ComputeInstancesCreateResponse_builder{
					Object: publicv1.ComputeInstance_builder{
						Id: "instance-1",
						Status: publicv1.ComputeInstanceStatus_builder{
							State: publicv1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_STARTING,
						}.Build(),
					}.Build(),
				}.Build(), nil
			},
		}

		handler := handleCreateComputeInstance(instances)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateComputeInstanceInput{
			Name:        "demo-vm",
			CatalogItem: "catalog-item-1",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(output).To(Equal(CreateComputeInstanceOutput{
			ID:    "instance-1",
			State: "COMPUTE_INSTANCE_STATE_STARTING",
		}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
		Expect(capturedObject.GetMetadata().GetName()).To(Equal("demo-vm"))
		Expect(capturedObject.GetMetadata().GetTenant()).To(BeEmpty())
		Expect(capturedObject.GetSpec().GetCatalogItem().GetId()).To(Equal("catalog-item-1"))
	})

	It("Applies governed field overrides onto the compute instance spec", func() {
		var capturedObject *publicv1.ComputeInstance
		instances := &mockComputeInstancesClient{
			createFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesCreateRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesCreateResponse, error) {
				capturedObject = request.GetObject()
				return publicv1.ComputeInstancesCreateResponse_builder{
					Object: publicv1.ComputeInstance_builder{Id: "instance-1"}.Build(),
				}.Build(), nil
			},
		}

		handler := handleCreateComputeInstance(instances)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateComputeInstanceInput{
			Name:        "demo-vm",
			CatalogItem: "catalog-item-1",
			Set:         []string{"ssh_public_key=ssh-ed25519 AAAA"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(capturedObject.GetSpec().GetSshPublicKey()).To(Equal("ssh-ed25519 AAAA"))
	})

	It("Rejects a malformed field override before creating a compute instance", func() {
		instances := &mockComputeInstancesClient{
			createFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesCreateRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesCreateResponse, error) {
				return nil, errors.New("Create should not be called")
			},
		}

		handler := handleCreateComputeInstance(instances)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateComputeInstanceInput{
			Name:        "demo-vm",
			CatalogItem: "catalog-item-1",
			Set:         []string{"missing-equals-sign"},
		})
		Expect(err).To(HaveOccurred())
	})

	It("Propagates a ComputeInstance Create error", func() {
		instances := &mockComputeInstancesClient{
			createFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesCreateRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesCreateResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleCreateComputeInstance(instances)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), CreateComputeInstanceInput{
			Name:        "demo-vm",
			CatalogItem: "catalog-item-1",
		})
		Expect(err).To(MatchError(ContainSubstring("boom")))
	})
})

var _ = Describe("handleDeleteComputeInstance", func() {
	It("Deletes the requested compute instance and forwards the caller token", func() {
		var capturedToken string
		instances := &mockComputeInstancesClient{
			deleteFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesDeleteRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesDeleteResponse, error) {
				capturedToken = forwardedToken(ctx)
				Expect(request.GetId()).To(Equal("instance-1"))
				return publicv1.ComputeInstancesDeleteResponse_builder{}.Build(), nil
			},
		}

		handler := handleDeleteComputeInstance(instances)
		_, output, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DeleteComputeInstanceInput{ID: "instance-1"})
		Expect(err).ToNot(HaveOccurred())
		Expect(output).To(Equal(DeleteComputeInstanceOutput{ID: "instance-1"}))
		Expect(capturedToken).To(Equal("Bearer raw-bearer-value"))
	})

	It("Propagates a ComputeInstance Delete error", func() {
		instances := &mockComputeInstancesClient{
			deleteFunc: func(
				ctx context.Context, request *publicv1.ComputeInstancesDeleteRequest, options ...grpc.CallOption,
			) (*publicv1.ComputeInstancesDeleteResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := handleDeleteComputeInstance(instances)
		_, _, err := handler(context.Background(), requestWithToken("raw-bearer-value"), DeleteComputeInstanceInput{ID: "instance-1"})
		Expect(err).To(MatchError(ContainSubstring("boom")))
	})
})
