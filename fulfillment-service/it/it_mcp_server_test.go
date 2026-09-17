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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/osac-project/osac/fulfillment-service/internal/auth"
	"github.com/osac-project/osac/fulfillment-service/internal/cmd/service/start/mcpserver"
	"github.com/osac-project/osac/fulfillment-service/internal/network"
	"github.com/osac-project/osac/fulfillment-service/internal/uuid"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

const defaultNetworkLabel = "osac.openshift.io/default"

var _ = Describe("MCP server", func() {
	var (
		ctx                            context.Context
		catalogItemsClient             privatev1.ComputeInstanceCatalogItemsClient
		computeInstanceTemplatesClient privatev1.ComputeInstanceTemplatesClient
		computeInstancesClient         publicv1.ComputeInstancesClient
		diskImagesClient               privatev1.DiskImagesClient
		instanceTypesClient            privatev1.InstanceTypesClient
		storageBackendsClient          privatev1.StorageBackendsClient
		storageTiersClient             privatev1.StorageTiersClient
		subnetsClient                  privatev1.SubnetsClient
		virtualNetworksClient          privatev1.VirtualNetworksClient
		networkClassesClient           privatev1.NetworkClassesClient

		catalogItemID     string
		computeInstanceID string
		templateID        string
		diskImageID       string
		instanceTypeID    string
		storageBackendID  string
		storageTierID     string
		subnetID          string
		virtualNetworkID  string
		networkClassID    string
		mcpGrpcConn       *grpc.ClientConn
		mcpHTTPServer     *httptest.Server
		mcpClient         *http.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		adminConn := tool.InternalView().AdminConn()
		catalogItemsClient = privatev1.NewComputeInstanceCatalogItemsClient(adminConn)
		computeInstanceTemplatesClient = privatev1.NewComputeInstanceTemplatesClient(adminConn)
		computeInstancesClient = publicv1.NewComputeInstancesClient(tool.ExternalView().UserConn())
		diskImagesClient = privatev1.NewDiskImagesClient(adminConn)
		instanceTypesClient = privatev1.NewInstanceTypesClient(adminConn)
		storageBackendsClient = privatev1.NewStorageBackendsClient(adminConn)
		storageTiersClient = privatev1.NewStorageTiersClient(adminConn)
		subnetsClient = privatev1.NewSubnetsClient(adminConn)
		virtualNetworksClient = privatev1.NewVirtualNetworksClient(adminConn)
		networkClassesClient = privatev1.NewNetworkClassesClient(adminConn)

		storageBackendResponse, err := storageBackendsClient.Create(ctx, privatev1.StorageBackendsCreateRequest_builder{
			Object: privatev1.StorageBackend_builder{
				Metadata: privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-storage-backend-%s", uuid.New()[24:32])}.Build(),
				Spec: privatev1.StorageBackendSpec_builder{
					Provider:    "test",
					Description: "Storage backend for MCP compute instance integration testing",
					Endpoint:    "https://test-backend.example.com",
					Credentials: privatev1.StorageBackendCredentials_builder{
						Username: "test-user",
						Password: "test-credential", //nolint:goconst // test-only fake provider credential
					}.Build(),
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		storageBackendID = storageBackendResponse.GetObject().GetId()

		storageTierResponse, err := storageTiersClient.Create(ctx, privatev1.StorageTiersCreateRequest_builder{
			Object: privatev1.StorageTier_builder{
				Metadata: privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-storage-tier-%s", uuid.New()[24:32])}.Build(),
				Spec: privatev1.StorageTierSpec_builder{
					Description: "Storage tier for MCP compute instance integration testing",
					Protocol:    privatev1.StorageProtocol_STORAGE_PROTOCOL_BLOCK,
					Backends: []*privatev1.BackendAssociation{
						privatev1.BackendAssociation_builder{BackendId: storageBackendID}.Build(),
					},
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		storageTierID = storageTierResponse.GetObject().GetId()

		instanceTypeResponse, err := instanceTypesClient.Create(ctx, privatev1.InstanceTypesCreateRequest_builder{
			Object: privatev1.InstanceType_builder{
				Metadata: privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-instance-type-%s", uuid.New()[24:32])}.Build(),
				Spec:     privatev1.InstanceTypeSpec_builder{Cores: 2, MemoryGib: 4}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		instanceTypeID = instanceTypeResponse.GetObject().GetId()

		diskImageResponse, err := diskImagesClient.Create(ctx, privatev1.DiskImagesCreateRequest_builder{
			Object: privatev1.DiskImage_builder{
				Metadata: privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-disk-image-%s", uuid.New()[24:32])}.Build(),
				Spec: privatev1.DiskImageSpec_builder{
					SourceType:    privatev1.SourceType_SOURCE_TYPE_REGISTRY,
					SourceRef:     "quay.io/containerdisks/fedora:41",
					GuestOsFamily: privatev1.GuestOSFamily_GUEST_OS_FAMILY_LINUX,
					Architecture:  []privatev1.Architecture{privatev1.Architecture_ARCHITECTURE_AMD64},
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		diskImageID = diskImageResponse.GetObject().GetId()

		templateID = fmt.Sprintf("mcp-compute-template-%s", uuid.New())
		_, err = computeInstanceTemplatesClient.Create(ctx, privatev1.ComputeInstanceTemplatesCreateRequest_builder{
			Object: privatev1.ComputeInstanceTemplate_builder{
				Id:          templateID,
				Metadata:    privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-compute-template-%s", uuid.New()[24:32])}.Build(),
				Title:       "MCP compute instance template",
				Description: "Template for MCP compute instance integration testing",
				SpecDefaults: privatev1.ComputeInstanceTemplateSpecDefaults_builder{
					InstanceType: privatev1.InstanceTypeReference_builder{Id: instanceTypeID}.Build(),
					DiskImage:    privatev1.DiskImageReference_builder{Id: diskImageID}.Build(),
					BootDisk: privatev1.ComputeInstanceDisk_builder{
						SizeGib:     proto.Int32(20),
						StorageTier: privatev1.StorageTierReference_builder{Id: storageTierID}.Build(),
					}.Build(),
					RunStrategy: privatev1.ComputeInstanceRunStrategy_COMPUTE_INSTANCE_RUN_STRATEGY_ALWAYS.Enum(),
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())

		catalogItemID = fmt.Sprintf("mcp-compute-catalog-item-%s", uuid.New())
		_, err = catalogItemsClient.Create(ctx, privatev1.ComputeInstanceCatalogItemsCreateRequest_builder{
			Object: privatev1.ComputeInstanceCatalogItem_builder{
				Id:          catalogItemID,
				Metadata:    privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-compute-catalog-item-%s", uuid.New()[24:32])}.Build(),
				Title:       "MCP compute instance",
				Description: "Catalog item for MCP compute instance integration testing",
				Published:   true,
				Template:    privatev1.ComputeInstanceTemplateReference_builder{Id: templateID}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())

		networkClassResponse, err := networkClassesClient.Create(ctx, privatev1.NetworkClassesCreateRequest_builder{
			Object: privatev1.NetworkClass_builder{
				Metadata:      privatev1.Metadata_builder{Name: fmt.Sprintf("mcp-network-class-%s", uuid.New()[24:32])}.Build(),
				Title:         "MCP network class",
				FabricManager: proto.String("netris"),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		networkClassID = networkClassResponse.GetObject().GetId()

		virtualNetworkID = fmt.Sprintf("mcp-virtual-network-%s", uuid.New())
		_, err = virtualNetworksClient.Create(ctx, privatev1.VirtualNetworksCreateRequest_builder{
			Object: privatev1.VirtualNetwork_builder{
				Id: virtualNetworkID,
				Metadata: privatev1.Metadata_builder{
					Name:   fmt.Sprintf("mcp-virtual-network-%s", uuid.New()[24:32]),
					Tenant: usersGroup,
				}.Build(),
				Spec: privatev1.VirtualNetworkSpec_builder{
					NetworkClass: privatev1.NetworkClassReference_builder{Id: networkClassID}.Build(),
					Region:       "us-east-1",
					Ipv4Cidr:     proto.String("10.200.0.0/16"),
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		Eventually(func(g Gomega) {
			response, err := virtualNetworksClient.Get(ctx, privatev1.VirtualNetworksGetRequest_builder{Id: virtualNetworkID}.Build())
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(response.GetObject().GetStatus().GetState()).To(Equal(privatev1.VirtualNetworkState_VIRTUAL_NETWORK_STATE_PENDING))
		}, time.Minute, time.Second).Should(Succeed())
		virtualNetworkResponse, err := virtualNetworksClient.Get(ctx, privatev1.VirtualNetworksGetRequest_builder{Id: virtualNetworkID}.Build())
		Expect(err).ToNot(HaveOccurred())
		virtualNetwork := virtualNetworkResponse.GetObject()
		virtualNetwork.SetStatus(privatev1.VirtualNetworkStatus_builder{State: privatev1.VirtualNetworkState_VIRTUAL_NETWORK_STATE_READY}.Build())
		_, err = virtualNetworksClient.Update(ctx, privatev1.VirtualNetworksUpdateRequest_builder{
			Object:     virtualNetwork,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"status.state"}},
		}.Build())
		Expect(err).ToNot(HaveOccurred())

		subnetID = fmt.Sprintf("mcp-subnet-%s", uuid.New())
		_, err = subnetsClient.Create(ctx, privatev1.SubnetsCreateRequest_builder{
			Object: privatev1.Subnet_builder{
				Id: subnetID,
				Metadata: privatev1.Metadata_builder{
					Name:   fmt.Sprintf("mcp-subnet-%s", uuid.New()[24:32]),
					Tenant: usersGroup,
					Labels: map[string]string{defaultNetworkLabel: "true"},
				}.Build(),
				Spec: privatev1.SubnetSpec_builder{
					VirtualNetwork: privatev1.VirtualNetworkLocalReference_builder{Id: virtualNetworkID}.Build(),
					Ipv4Cidr:       proto.String("10.200.1.0/24"),
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		Eventually(func(g Gomega) {
			response, err := subnetsClient.Get(ctx, privatev1.SubnetsGetRequest_builder{Id: subnetID}.Build())
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(response.GetObject().GetStatus().GetState()).To(Equal(privatev1.SubnetState_SUBNET_STATE_PENDING))
		}, time.Minute, time.Second).Should(Succeed())
		subnetResponse, err := subnetsClient.Get(ctx, privatev1.SubnetsGetRequest_builder{Id: subnetID}.Build())
		Expect(err).ToNot(HaveOccurred())
		subnet := subnetResponse.GetObject()
		subnet.SetStatus(privatev1.SubnetStatus_builder{State: privatev1.SubnetState_SUBNET_STATE_READY}.Build())
		_, err = subnetsClient.Update(ctx, privatev1.SubnetsUpdateRequest_builder{
			Object:     subnet,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"status.state"}},
		}.Build())
		Expect(err).ToNot(HaveOccurred())

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

		mcpGrpcConn, err = network.NewGrpcClient().
			SetLogger(logger).
			SetCaPool(tool.CaPool()).
			SetAddress(externalServiceAddr).
			SetUserAgent("fulfillment-mcp-server-it").
			Build()
		Expect(err).ToNot(HaveOccurred())
		handler, err := mcpserver.NewHandler(mcpserver.ServerDeps{
			ComputeInstanceCatalogItemsClient: publicv1.NewComputeInstanceCatalogItemsClient(mcpGrpcConn),
			ComputeInstancesClient:            publicv1.NewComputeInstancesClient(mcpGrpcConn),
		}, jwtValidator, "", "")
		Expect(err).ToNot(HaveOccurred())
		mcpHTTPServer = httptest.NewServer(handler)
		mcpClient = &http.Client{
			Transport: ghttp.RoundTripperFunc(func(request *http.Request) (*http.Response, error) {
				token, err := tool.UserTokenSource().Token(request.Context())
				if err != nil {
					return nil, err
				}
				request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.Access))
				return http.DefaultTransport.RoundTrip(request)
			}),
		}
	})

	AfterEach(func() {
		if mcpHTTPServer != nil {
			mcpHTTPServer.Close()
		}
		if mcpGrpcConn != nil {
			Expect(mcpGrpcConn.Close()).ToNot(HaveOccurred())
		}
		if computeInstanceID != "" {
			_, err := computeInstancesClient.Delete(ctx, publicv1.ComputeInstancesDeleteRequest_builder{Id: computeInstanceID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if subnetID != "" {
			subnetResponse, err := subnetsClient.Get(ctx, privatev1.SubnetsGetRequest_builder{Id: subnetID}.Build())
			Expect(err).ToNot(HaveOccurred())
			subnet := subnetResponse.GetObject()
			labels := subnet.GetMetadata().GetLabels()
			delete(labels, defaultNetworkLabel)
			subnet.GetMetadata().SetLabels(labels)
			_, err = subnetsClient.Update(ctx, privatev1.SubnetsUpdateRequest_builder{
				Object:     subnet,
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata.labels"}},
			}.Build())
			Expect(err).ToNot(HaveOccurred())
			_, err = subnetsClient.Delete(ctx, privatev1.SubnetsDeleteRequest_builder{Id: subnetID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if virtualNetworkID != "" {
			_, err := virtualNetworksClient.Delete(ctx, privatev1.VirtualNetworksDeleteRequest_builder{Id: virtualNetworkID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if networkClassID != "" {
			_, err := networkClassesClient.Delete(ctx, privatev1.NetworkClassesDeleteRequest_builder{Id: networkClassID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if catalogItemID != "" {
			_, err := catalogItemsClient.Delete(ctx, privatev1.ComputeInstanceCatalogItemsDeleteRequest_builder{Id: catalogItemID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if templateID != "" {
			_, err := computeInstanceTemplatesClient.Delete(ctx, privatev1.ComputeInstanceTemplatesDeleteRequest_builder{Id: templateID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if instanceTypeID != "" {
			_, err := instanceTypesClient.Delete(ctx, privatev1.InstanceTypesDeleteRequest_builder{Id: instanceTypeID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if storageTierID != "" {
			_, err := storageTiersClient.Delete(ctx, privatev1.StorageTiersDeleteRequest_builder{Id: storageTierID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if storageBackendID != "" {
			_, err := storageBackendsClient.Delete(ctx, privatev1.StorageBackendsDeleteRequest_builder{Id: storageBackendID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
		if diskImageID != "" {
			_, err := diskImagesClient.Delete(ctx, privatev1.DiskImagesDeleteRequest_builder{Id: diskImageID}.Build())
			Expect(err).ToNot(HaveOccurred())
		}
	})

	It("drives ComputeInstance discovery, creation, inspection, and deletion over real HTTP with bearer token forwarding", func() {
		transport := &mcp.StreamableClientTransport{Endpoint: mcpHTTPServer.URL, HTTPClient: mcpClient}
		client := mcp.NewClient(&mcp.Implementation{Name: "it-mcp-client", Version: "0.1.0"}, nil)
		session, err := client.Connect(ctx, transport, nil)
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(session.Close()).To(Succeed()) }()

		listOutput, err := callMCPTool[mcpserver.ListResourcesOutput](ctx, session, "list_resources", mcpserver.ListResourcesInput{
			ResourceType: mcpserver.ResourceTypeComputeInstanceCatalogItem,
			Filter:       fmt.Sprintf("this.id == %q", catalogItemID),
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(listOutput.Items).To(HaveLen(1))
		Expect(listOutput.Items[0].ID).To(Equal(catalogItemID))

		catalogItem, err := callMCPTool[mcpserver.GetResourceOutput](ctx, session, "get_resource", mcpserver.GetResourceInput{
			ResourceType: mcpserver.ResourceTypeComputeInstanceCatalogItem,
			ID:           catalogItemID,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(catalogItem.Resource).To(HaveKeyWithValue("id", catalogItemID))
		Expect(catalogItem.Resource).To(HaveKeyWithValue("title", "MCP compute instance"))

		created, err := callMCPTool[mcpserver.CreateComputeInstanceFromCatalogItemOutput](
			ctx, session, "create_compute_instance_from_catalog_item", mcpserver.CreateComputeInstanceFromCatalogItemInput{
				Name:        fmt.Sprintf("mcp-compute-instance-%s", uuid.New()[24:32]),
				CatalogItem: catalogItemID,
			},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(created.ID).ToNot(BeEmpty())
		computeInstanceID = created.ID

		instance, err := callMCPTool[mcpserver.GetResourceOutput](ctx, session, "get_resource", mcpserver.GetResourceInput{
			ResourceType: mcpserver.ResourceTypeComputeInstance,
			ID:           computeInstanceID,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(instance.Resource).To(HaveKeyWithValue("id", computeInstanceID))

		usersClient := privatev1.NewUsersClient(tool.InternalView().AdminConn())
		usersResponse, err := usersClient.List(ctx, privatev1.UsersListRequest_builder{
			Filter: proto.String(fmt.Sprintf("this.spec.username == %q", userUsername)),
			Limit:  proto.Int32(1),
		}.Build())
		Expect(err).ToNot(HaveOccurred())
		Expect(usersResponse.GetSize()).To(Equal(int32(1)))
		createdInstance, err := computeInstancesClient.Get(ctx, publicv1.ComputeInstancesGetRequest_builder{Id: computeInstanceID}.Build())
		Expect(err).ToNot(HaveOccurred())
		Expect(createdInstance.GetObject().GetMetadata().GetCreator()).To(Equal(usersResponse.GetItems()[0].GetId()))
		Expect(createdInstance.GetObject().GetMetadata().GetTenant()).To(Equal(usersGroup))
		Expect(createdInstance.GetObject().GetSpec().GetNetworkAttachments()).To(HaveLen(1))
		Expect(createdInstance.GetObject().GetSpec().GetNetworkAttachments()[0].GetSubnet().GetId()).To(Equal(subnetID))

		deleted, err := callMCPTool[mcpserver.DeleteComputeInstanceOutput](
			ctx, session, "delete_compute_instance", mcpserver.DeleteComputeInstanceInput{ID: computeInstanceID},
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(deleted.ID).To(Equal(computeInstanceID))
		computeInstanceID = ""
	})
})

func callMCPTool[Out any](ctx context.Context, session *mcp.ClientSession, name string, arguments any) (Out, error) {
	var output Out
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return output, fmt.Errorf("failed to call tool %q: %w", name, err)
	}
	if result.IsError {
		return output, fmt.Errorf("tool %q returned an error result: %+v", name, result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return output, fmt.Errorf("failed to marshal structured content from tool %q: %w", name, err)
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, fmt.Errorf("failed to unmarshal structured content from tool %q: %w", name, err)
	}
	return output, nil
}
