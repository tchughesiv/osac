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

	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
	"github.com/osac-project/osac/fulfillment-service/internal/cmd/cli/create/fieldutil"
)

// CreateClusterFromCatalogItemInput is the input for the create_cluster_from_catalog_item tool.
type CreateClusterFromCatalogItemInput struct {
	Name        string   `json:"name" jsonschema:"name for the new cluster"`
	CatalogItem string   `json:"catalog_item" jsonschema:"id or name of the catalog item to create the cluster from"`
	Set         []string `json:"set,omitempty" jsonschema:"optional key=value field overrides, matching the catalog item's field_definitions paths (dot notation)"`
}

// CreateClusterFromCatalogItemOutput is the output of the create_cluster_from_catalog_item tool.
type CreateClusterFromCatalogItemOutput struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// GetClusterStatusInput is the input for the get_cluster_status tool.
type GetClusterStatusInput struct {
	ID string `json:"id" jsonschema:"the cluster's id"`
}

// ConditionSummary is a summary of one cluster status condition, as returned by get_cluster_status.
type ConditionSummary struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// GetClusterStatusOutput is the output of the get_cluster_status tool.
type GetClusterStatusOutput struct {
	ID         string             `json:"id"`
	State      string             `json:"state"`
	Conditions []ConditionSummary `json:"conditions"`
	APIURL     string             `json:"api_url,omitempty"`
	ConsoleURL string             `json:"console_url,omitempty"`
}

// handleCreateClusterFromCatalogItem returns the handler for the create_cluster_from_catalog_item tool.
func handleCreateClusterFromCatalogItem(
	client publicv1.ClustersClient,
) mcp.ToolHandlerFor[CreateClusterFromCatalogItemInput, CreateClusterFromCatalogItemOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input CreateClusterFromCatalogItemInput,
	) (*mcp.CallToolResult, CreateClusterFromCatalogItemOutput, error) {
		ctx = forwardToken(ctx, req)

		// The server resolves this reference by id-or-name itself (see refKey/lookupCatalogItem in
		// internal/servers/private_clusters_server.go), so it's safe to pass either into Id here without a
		// separate client-side lookup — matching the CLI's own simpler style for this reference
		// (internal/cmd/cli/create/cluster/create_cluster_cmd.go).
		spec := publicv1.ClusterSpec_builder{
			CatalogItem: publicv1.ClusterCatalogItemReference_builder{Id: input.CatalogItem}.Build(),
		}.Build()
		if err := fieldutil.ApplyFields(spec, input.Set); err != nil {
			return nil, CreateClusterFromCatalogItemOutput{}, fmt.Errorf("failed to apply field overrides: %w", err)
		}

		// metadata.tenant and metadata.creator are deliberately left unset: the server assigns both from the
		// caller's own forwarded identity (see GenericServer.prepareForCreate), which is the attribution
		// behavior this tool exists to demonstrate.
		cluster := publicv1.Cluster_builder{
			Metadata: publicv1.Metadata_builder{Name: input.Name}.Build(),
			Spec:     spec,
		}.Build()
		response, err := client.Create(ctx, publicv1.ClustersCreateRequest_builder{
			Object: cluster,
		}.Build())
		if err != nil {
			return nil, CreateClusterFromCatalogItemOutput{}, fmt.Errorf("failed to create cluster: %w", err)
		}

		created := response.GetObject()
		return nil, CreateClusterFromCatalogItemOutput{
			ID:    created.GetId(),
			State: created.GetStatus().GetState().String(),
		}, nil
	}
}

// handleGetClusterStatus returns the handler for the get_cluster_status tool.
func handleGetClusterStatus(client publicv1.ClustersClient) mcp.ToolHandlerFor[GetClusterStatusInput, GetClusterStatusOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input GetClusterStatusInput,
	) (*mcp.CallToolResult, GetClusterStatusOutput, error) {
		ctx = forwardToken(ctx, req)
		response, err := client.Get(ctx, publicv1.ClustersGetRequest_builder{
			Id: input.ID,
		}.Build())
		if err != nil {
			return nil, GetClusterStatusOutput{}, fmt.Errorf("failed to get cluster '%s': %w", input.ID, err)
		}

		status := response.GetObject().GetStatus()
		rawConditions := status.GetConditions()
		conditions := make([]ConditionSummary, len(rawConditions))
		for i, condition := range rawConditions {
			conditions[i] = ConditionSummary{
				Type:    condition.GetType().String(),
				Status:  condition.GetStatus().String(),
				Message: condition.GetMessage(),
			}
		}
		return nil, GetClusterStatusOutput{
			ID:         response.GetObject().GetId(),
			State:      status.GetState().String(),
			Conditions: conditions,
			APIURL:     status.GetApiUrl(),
			ConsoleURL: status.GetConsoleUrl(),
		}, nil
	}
}
