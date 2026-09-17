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
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

type ResourceType string

const (
	ResourceTypeComputeInstanceCatalogItem ResourceType = "compute_instance_catalog_item"
	ResourceTypeComputeInstance            ResourceType = "compute_instance"
)

type ListResourcesInput struct {
	ResourceType ResourceType `json:"resource_type" jsonschema:"compute_instance_catalog_item or compute_instance"`
	Filter       string       `json:"filter,omitempty" jsonschema:"optional CEL filter expression to narrow results"`
	Offset       int32        `json:"offset,omitempty" jsonschema:"zero-based result offset"`
	PageSize     int32        `json:"page_size,omitempty" jsonschema:"maximum result count from 1 through 100"`
}

type ResourceSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state,omitempty"`
}

type ListResourcesOutput struct {
	Offset int32             `json:"offset"`
	Size   int32             `json:"size"`
	Total  int32             `json:"total"`
	Items  []ResourceSummary `json:"items"`
}

type GetResourceInput struct {
	ResourceType ResourceType `json:"resource_type" jsonschema:"compute_instance_catalog_item or compute_instance"`
	ID           string       `json:"id" jsonschema:"resource ID"`
}

type GetResourceOutput struct {
	Resource map[string]any `json:"resource"`
}

func handleListResources(
	catalogItems publicv1.ComputeInstanceCatalogItemsClient, instances publicv1.ComputeInstancesClient,
) mcp.ToolHandlerFor[ListResourcesInput, ListResourcesOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input ListResourcesInput,
	) (*mcp.CallToolResult, ListResourcesOutput, error) {
		page, err := resourcePage(input)
		if err != nil {
			return nil, ListResourcesOutput{}, err
		}
		ctx = forwardToken(ctx, req)
		switch input.ResourceType {
		case ResourceTypeComputeInstanceCatalogItem:
			request := publicv1.ComputeInstanceCatalogItemsListRequest_builder{}
			if input.Filter != "" {
				request.Filter = proto.String(input.Filter)
			}
			request.Offset = proto.Int32(page.offset)
			request.Limit = proto.Int32(page.limit)
			response, err := catalogItems.List(ctx, request.Build())
			if err != nil {
				return nil, ListResourcesOutput{}, fmt.Errorf("failed to list compute instance catalog items: %w", err)
			}
			items := make([]ResourceSummary, len(response.GetItems()))
			for i, item := range response.GetItems() {
				items[i] = ResourceSummary{
					ID:          item.GetId(),
					Name:        item.GetMetadata().GetName(),
					Title:       item.GetTitle(),
					Description: item.GetDescription(),
				}
			}
			return nil, ListResourcesOutput{
				Offset: page.offset,
				Size:   response.GetSize(),
				Total:  response.GetTotal(),
				Items:  items,
			}, nil
		case ResourceTypeComputeInstance:
			request := publicv1.ComputeInstancesListRequest_builder{}
			if input.Filter != "" {
				request.Filter = proto.String(input.Filter)
			}
			request.Offset = proto.Int32(page.offset)
			request.Limit = proto.Int32(page.limit)
			response, err := instances.List(ctx, request.Build())
			if err != nil {
				return nil, ListResourcesOutput{}, fmt.Errorf("failed to list compute instances: %w", err)
			}
			items := make([]ResourceSummary, len(response.GetItems()))
			for i, item := range response.GetItems() {
				items[i] = ResourceSummary{
					ID:    item.GetId(),
					Name:  item.GetMetadata().GetName(),
					State: item.GetStatus().GetState().String(),
				}
			}
			return nil, ListResourcesOutput{
				Offset: page.offset,
				Size:   response.GetSize(),
				Total:  response.GetTotal(),
				Items:  items,
			}, nil
		default:
			return nil, ListResourcesOutput{}, unsupportedResourceTypeError(input.ResourceType)
		}
	}
}

const (
	defaultResourcePageSize int32 = 50
	maxResourcePageSize     int32 = 100
)

type resourcePageRequest struct {
	offset int32
	limit  int32
}

func resourcePage(input ListResourcesInput) (resourcePageRequest, error) {
	if input.Offset < 0 {
		return resourcePageRequest{}, fmt.Errorf("offset must not be negative")
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = defaultResourcePageSize
	}
	if pageSize < 0 || pageSize > maxResourcePageSize {
		return resourcePageRequest{}, fmt.Errorf("page_size must be between 1 and %d", maxResourcePageSize)
	}
	return resourcePageRequest{offset: input.Offset, limit: pageSize}, nil
}

func handleGetResource(
	catalogItems publicv1.ComputeInstanceCatalogItemsClient, instances publicv1.ComputeInstancesClient,
) mcp.ToolHandlerFor[GetResourceInput, GetResourceOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input GetResourceInput,
	) (*mcp.CallToolResult, GetResourceOutput, error) {
		ctx = forwardToken(ctx, req)
		switch input.ResourceType {
		case ResourceTypeComputeInstanceCatalogItem:
			response, err := catalogItems.Get(ctx, publicv1.ComputeInstanceCatalogItemsGetRequest_builder{Id: input.ID}.Build())
			if err != nil {
				return nil, GetResourceOutput{}, fmt.Errorf("failed to get compute instance catalog item %q: %w", input.ID, err)
			}
			resource, err := messageToMap(response.GetObject())
			if err != nil {
				return nil, GetResourceOutput{}, fmt.Errorf("failed to encode compute instance catalog item: %w", err)
			}
			return nil, GetResourceOutput{Resource: resource}, nil
		case ResourceTypeComputeInstance:
			response, err := instances.Get(ctx, publicv1.ComputeInstancesGetRequest_builder{Id: input.ID}.Build())
			if err != nil {
				return nil, GetResourceOutput{}, fmt.Errorf("failed to get compute instance %q: %w", input.ID, err)
			}
			resource, err := messageToMap(response.GetObject())
			if err != nil {
				return nil, GetResourceOutput{}, fmt.Errorf("failed to encode compute instance: %w", err)
			}
			return nil, GetResourceOutput{Resource: resource}, nil
		default:
			return nil, GetResourceOutput{}, unsupportedResourceTypeError(input.ResourceType)
		}
	}
}

func unsupportedResourceTypeError(resourceType ResourceType) error {
	return fmt.Errorf("unsupported resource type %q", resourceType)
}

func messageToMap(message proto.Message) (map[string]any, error) {
	if message == nil {
		return map[string]any{}, nil
	}
	encoded, err := protojson.Marshal(message)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}
