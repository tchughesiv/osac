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
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/osac-project/osac/fulfillment-service/internal/cmd/cli/create/fieldutil"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

type CreateComputeInstanceFromCatalogItemInput struct {
	Name        string   `json:"name" jsonschema:"name for the new compute instance"`
	CatalogItem string   `json:"catalog_item" jsonschema:"ID of the compute instance catalog item"`
	Set         []string `json:"set,omitempty" jsonschema:"optional key=value field overrides governed by the catalog item"`
}

type CreateComputeInstanceFromCatalogItemOutput struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type DeleteComputeInstanceInput struct {
	ID string `json:"id" jsonschema:"compute instance ID"`
}

type DeleteComputeInstanceOutput struct {
	ID string `json:"id"`
}

func handleCreateComputeInstanceFromCatalogItem(
	client publicv1.ComputeInstancesClient,
) mcp.ToolHandlerFor[CreateComputeInstanceFromCatalogItemInput, CreateComputeInstanceFromCatalogItemOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input CreateComputeInstanceFromCatalogItemInput,
	) (*mcp.CallToolResult, CreateComputeInstanceFromCatalogItemOutput, error) {
		ctx = forwardToken(ctx, req)
		spec := publicv1.ComputeInstanceSpec_builder{
			CatalogItem: publicv1.ComputeInstanceCatalogItemReference_builder{Id: input.CatalogItem}.Build(),
		}.Build()
		if err := fieldutil.ApplyFields(spec, input.Set); err != nil {
			return nil, CreateComputeInstanceFromCatalogItemOutput{}, fmt.Errorf("failed to apply field overrides: %w", err)
		}
		response, err := client.Create(ctx, publicv1.ComputeInstancesCreateRequest_builder{
			Object: publicv1.ComputeInstance_builder{
				Metadata: publicv1.Metadata_builder{Name: input.Name}.Build(),
				Spec:     spec,
			}.Build(),
		}.Build())
		if err != nil {
			return nil, CreateComputeInstanceFromCatalogItemOutput{}, fmt.Errorf("failed to create compute instance: %w", err)
		}
		created := response.GetObject()
		return nil, CreateComputeInstanceFromCatalogItemOutput{
			ID:    created.GetId(),
			State: created.GetStatus().GetState().String(),
		}, nil
	}
}

func handleDeleteComputeInstance(
	client publicv1.ComputeInstancesClient,
) mcp.ToolHandlerFor[DeleteComputeInstanceInput, DeleteComputeInstanceOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input DeleteComputeInstanceInput,
	) (*mcp.CallToolResult, DeleteComputeInstanceOutput, error) {
		ctx = forwardToken(ctx, req)
		_, err := client.Delete(ctx, publicv1.ComputeInstancesDeleteRequest_builder{Id: input.ID}.Build())
		if err != nil {
			return nil, DeleteComputeInstanceOutput{}, fmt.Errorf("failed to delete compute instance %q: %w", input.ID, err)
		}
		return nil, DeleteComputeInstanceOutput(input), nil
	}
}
