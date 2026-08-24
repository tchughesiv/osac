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
	"google.golang.org/protobuf/proto"

	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
)

// ListCatalogItemsInput is the input for the list_catalog_items tool.
type ListCatalogItemsInput struct {
	Filter string `json:"filter,omitempty" jsonschema:"optional CEL filter expression to narrow results"`
}

// CatalogItemSummary is a summary of a cluster catalog item, as returned by list_catalog_items.
type CatalogItemSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ListCatalogItemsOutput is the output of the list_catalog_items tool.
type ListCatalogItemsOutput struct {
	Items []CatalogItemSummary `json:"items"`
}

// DescribeCatalogItemInput is the input for the describe_catalog_item tool.
type DescribeCatalogItemInput struct {
	ID string `json:"id" jsonschema:"the catalog item's id or name"`
}

// FieldDefinitionSummary is a summary of one field a catalog item allows the caller to control, as returned by
// describe_catalog_item.
type FieldDefinitionSummary struct {
	Path         string `json:"path"`
	DisplayName  string `json:"display_name"`
	Editable     bool   `json:"editable"`
	DefaultValue string `json:"default_value,omitempty"`
}

// DescribeCatalogItemOutput is the output of the describe_catalog_item tool.
type DescribeCatalogItemOutput struct {
	ID               string                   `json:"id"`
	Title            string                   `json:"title"`
	Description      string                   `json:"description"`
	FieldDefinitions []FieldDefinitionSummary `json:"field_definitions"`
}

// handleListCatalogItems returns the handler for the list_catalog_items tool.
func handleListCatalogItems(
	client publicv1.ClusterCatalogItemsClient,
) mcp.ToolHandlerFor[ListCatalogItemsInput, ListCatalogItemsOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input ListCatalogItemsInput,
	) (*mcp.CallToolResult, ListCatalogItemsOutput, error) {
		ctx = forwardToken(ctx, req)
		requestBuilder := publicv1.ClusterCatalogItemsListRequest_builder{}
		if input.Filter != "" {
			requestBuilder.Filter = proto.String(input.Filter)
		}
		response, err := client.List(ctx, requestBuilder.Build())
		if err != nil {
			return nil, ListCatalogItemsOutput{}, fmt.Errorf("failed to list catalog items: %w", err)
		}
		matches := response.GetItems()
		items := make([]CatalogItemSummary, len(matches))
		for i, match := range matches {
			items[i] = CatalogItemSummary{
				ID:          match.GetId(),
				Title:       match.GetTitle(),
				Description: match.GetDescription(),
			}
		}
		return nil, ListCatalogItemsOutput{Items: items}, nil
	}
}

// handleDescribeCatalogItem returns the handler for the describe_catalog_item tool.
func handleDescribeCatalogItem(
	client publicv1.ClusterCatalogItemsClient,
) mcp.ToolHandlerFor[DescribeCatalogItemInput, DescribeCatalogItemOutput] {
	return func(
		ctx context.Context, req *mcp.CallToolRequest, input DescribeCatalogItemInput,
	) (*mcp.CallToolResult, DescribeCatalogItemOutput, error) {
		ctx = forwardToken(ctx, req)
		item, err := findCatalogItem(ctx, client, input.ID)
		if err != nil {
			return nil, DescribeCatalogItemOutput{}, err
		}
		definitions := item.GetFieldDefinitions()
		fieldDefinitions := make([]FieldDefinitionSummary, len(definitions))
		for i, definition := range definitions {
			fieldDefinitions[i] = FieldDefinitionSummary{
				Path:         definition.GetPath(),
				DisplayName:  definition.GetDisplayName(),
				Editable:     definition.GetEditable(),
				DefaultValue: defaultValueString(definition),
			}
		}
		return nil, DescribeCatalogItemOutput{
			ID:               item.GetId(),
			Title:            item.GetTitle(),
			Description:      item.GetDescription(),
			FieldDefinitions: fieldDefinitions,
		}, nil
	}
}

// findCatalogItem resolves a catalog item by id or metadata.name. The Get RPC only resolves by the server's internal
// id (see GenericServer.Get), but MCP callers commonly only know a catalog item's human-readable name, so this uses
// the same CEL-filter resolution pattern findTemplate/findVersion use in the CLI
// (internal/cmd/cli/create/cluster/create_cluster_cmd.go) to accept either.
func findCatalogItem(
	ctx context.Context, client publicv1.ClusterCatalogItemsClient, ref string,
) (*publicv1.ClusterCatalogItem, error) {
	filter := fmt.Sprintf("this.id == %[1]q || this.metadata.name == %[1]q", ref)
	response, err := client.List(ctx, publicv1.ClusterCatalogItemsListRequest_builder{
		Filter: proto.String(filter),
		Limit:  proto.Int32(2),
	}.Build())
	if err != nil {
		return nil, fmt.Errorf("failed to look up catalog item '%s': %w", ref, err)
	}
	matches := response.GetItems()
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("catalog item '%s' not found", ref)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("catalog item reference '%s' is ambiguous: matched %d items", ref, len(matches))
	}
}

// defaultValueString stringifies a field definition's default value, or returns an empty string if it has none.
func defaultValueString(definition *publicv1.FieldDefinition) string {
	value := definition.GetDefault().AsInterface()
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
