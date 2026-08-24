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
	"fmt"
	"log/slog"
	"net/http"
	"syscall"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/grpc/metadata"

	publicv1 "github.com/osac-project/osac/fulfillment-service/internal/api/osac/public/v1"
	"github.com/osac-project/osac/fulfillment-service/internal/auth"
	"github.com/osac-project/osac/fulfillment-service/internal/logging"
	"github.com/osac-project/osac/fulfillment-service/internal/network"
	shtdwn "github.com/osac-project/osac/fulfillment-service/internal/shutdown"
	"github.com/osac-project/osac/fulfillment-service/internal/version"
)

// Cmd creates and returns the `start mcp-server` command.
func Cmd() *cobra.Command {
	runner := &runnerContext{}
	command := &cobra.Command{
		Use:                   "mcp-server [FLAG...]",
		Short:                 shortHelp,
		Long:                  longHelp,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE:                  runner.run,
	}
	flags := command.Flags()
	network.AddListenerFlags(flags, network.HttpListenerName, network.DefaultHttpAddress)
	network.AddCorsFlags(flags, network.HttpListenerName)
	network.AddGrpcClientFlags(flags, network.GrpcClientName, network.DefaultGrpcAddress)
	flags.StringSliceVar(
		&runner.args.trustedTokenIssuers,
		"grpc-authn-trusted-token-issuers",
		[]string{},
		trustedTokenIssuersFlagHelp,
	)
	flags.StringSliceVar(
		&runner.args.caFiles,
		"ca-file",
		[]string{},
		caFileFlagHelp,
	)
	return command
}

// runnerContext contains the data and logic needed to run the `start mcp-server` command.
type runnerContext struct {
	logger *slog.Logger
	flags  *pflag.FlagSet
	args   struct {
		trustedTokenIssuers []string
		caFiles             []string
	}
}

// ServerDeps contains the downstream clients used by the MCP tool handlers. Exported so that it/'s integration test
// can build a handler pointed at a live fulfillment-service instance without duplicating newServer's tool
// registration.
type ServerDeps struct {
	CatalogItemsClient publicv1.ClusterCatalogItemsClient
	ClustersClient     publicv1.ClustersClient
}

// tokenExpirationLeeway is shared between the JWT validator and the bearer-token middleware's clock-skew
// tolerance, so the two expiration checks agree on how much slack to allow.
const tokenExpirationLeeway = 5 * time.Second

// run runs the `start mcp-server` command.
func (c *runnerContext) run(cmd *cobra.Command, argv []string) error {
	// Get the context:
	ctx, cancel := context.WithCancel(cmd.Context())

	// Get the dependencies from the context:
	c.logger = logging.LoggerFromContext(ctx)

	// Save the flags:
	c.flags = cmd.Flags()

	// Create the shutdown sequence:
	shutdown, err := shtdwn.NewSequence().
		SetLogger(c.logger).
		AddSignals(syscall.SIGTERM, syscall.SIGINT).
		AddContext("context", 0, cancel).
		Build()
	if err != nil {
		return fmt.Errorf("failed to create shutdown sequence: %w", err)
	}

	// Create the network listener:
	c.logger.InfoContext(ctx, "Creating MCP server listener")
	listener, err := network.NewListener().
		SetLogger(c.logger).
		SetFlags(c.flags, network.HttpListenerName).
		AddTLSProtocol("h2").
		AddTLSProtocol("http/1.1").
		Build()
	if err != nil {
		return err
	}

	// Load the trusted CA certificates:
	c.logger.InfoContext(ctx, "Loading trusted CA certificates")
	caPool, err := network.NewCertPool().
		SetLogger(c.logger).
		AddSystemFiles(true).
		AddKubernetesFiles(true).
		AddFiles(c.args.caFiles...).
		Build()
	if err != nil {
		return fmt.Errorf("failed to load trusted CA certificates: %w", err)
	}

	// Create the JWT validator used to verify incoming bearer tokens:
	c.logger.InfoContext(ctx, "Creating JWKS cache")
	jwksCache, err := auth.NewJwksCache().
		SetLogger(c.logger).
		SetCaPool(caPool).
		AddIssuers(c.args.trustedTokenIssuers...).
		AddKubernetesIssuer(true).
		Build()
	if err != nil {
		return fmt.Errorf("failed to create JWKS cache: %w", err)
	}
	c.logger.InfoContext(ctx, "Creating JWT validator")
	jwtValidator, err := auth.NewJwtValidator().
		SetLogger(c.logger).
		SetJwksCache(jwksCache).
		SetExpirationLeeway(tokenExpirationLeeway).
		Build()
	if err != nil {
		return fmt.Errorf("failed to create JWT validator: %w", err)
	}

	// Calculate the user agent:
	userAgent := fmt.Sprintf("%s/%s", userAgent, version.Get())

	// Create the downstream gRPC client. No token source is configured here: every call carries the caller's own
	// bearer token, forwarded by forwardToken, rather than a single fixed identity.
	c.logger.InfoContext(ctx, "Creating gRPC client")
	grpcClient, err := network.NewGrpcClient().
		SetLogger(c.logger).
		SetFlags(c.flags, network.GrpcClientName).
		SetCaPool(caPool).
		SetUserAgent(userAgent).
		Build()
	if err != nil {
		return err
	}

	// Build the MCP server and wrap it with bearer-token authentication:
	handler := NewHandler(ServerDeps{
		CatalogItemsClient: publicv1.NewClusterCatalogItemsClient(grpcClient),
		ClustersClient:     publicv1.NewClustersClient(grpcClient),
	}, jwtValidator)

	// Add the CORS support:
	corsMiddleware, err := network.NewCorsMiddleware().
		SetLogger(c.logger).
		SetFlags(c.flags, network.HttpListenerName).
		Build()
	if err != nil {
		return fmt.Errorf("failed to create CORS middleware: %w", err)
	}
	handler = corsMiddleware(handler)

	// Start serving:
	c.logger.InfoContext(
		ctx,
		"Start serving",
		slog.String("address", listener.Addr().String()),
	)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           handler,
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.logger.ErrorContext(
				ctx,
				"MCP server failed",
				slog.Any("error", err),
			)
		}
	}()
	shutdown.AddHttpServer(network.HttpListenerName, 0, httpServer)

	// Keep running till the shutdown sequence completes:
	c.logger.InfoContext(ctx, "Waiting for shutdown sequence to complete")
	return shutdown.Wait()
}

// newServer creates the MCP server and registers its tools.
func newServer(deps ServerDeps) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "osac-deployment-mcp",
		Version: version.Get(),
	}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_catalog_items",
		Description: "Lists the published cluster catalog items that clusters can be created from.",
	}, handleListCatalogItems(deps.CatalogItemsClient))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "describe_catalog_item",
		Description: "Describes a cluster catalog item, including the fields callers may set when creating a cluster from it.",
	}, handleDescribeCatalogItem(deps.CatalogItemsClient))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_cluster_from_catalog_item",
		Description: "Creates a new cluster from a cluster catalog item.",
	}, handleCreateClusterFromCatalogItem(deps.ClustersClient))
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_cluster_status",
		Description: "Gets the current status of a cluster.",
	}, handleGetClusterStatus(deps.ClustersClient))
	return server
}

// NewHandler builds the composed HTTP handler for the MCP server: tool registration (newServer), the Streamable
// HTTP transport, and bearer-token verification — the same composition run's RunE serves. Exported so it/'s
// integration test can stand up a real instance against a live fulfillment-service without re-deriving this wiring
// (and risking it drifting from the real command).
func NewHandler(deps ServerDeps, validator auth.JwtValidator) http.Handler {
	server := newServer(deps)
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless: true,
		},
	)
	return sdkauth.RequireBearerToken(newTokenVerifier(validator), &sdkauth.RequireBearerTokenOptions{
		// Matches the JWT validator's own expiration leeway, so the SDK's independent expiration check
		// doesn't reject tokens the validator itself still considers valid.
		ClockSkew: tokenExpirationLeeway,
	})(streamableHandler)
}

// rawTokenExtraKey is the key used to stash the raw bearer token string inside sdkauth.TokenInfo.Extra, since
// TokenInfo itself doesn't retain the original token.
const rawTokenExtraKey = "raw_token"

// newTokenVerifier adapts an auth.JwtValidator to the shape the MCP SDK's bearer-token middleware requires.
func newTokenVerifier(validator auth.JwtValidator) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		parsed, err := validator.Validate(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", sdkauth.ErrInvalidToken, err)
		}
		subject, err := parsed.Claims.GetSubject()
		if err != nil || subject == "" {
			return nil, fmt.Errorf("%w: token has no subject claim", sdkauth.ErrInvalidToken)
		}
		expiration, err := parsed.Claims.GetExpirationTime()
		if err != nil || expiration == nil {
			return nil, fmt.Errorf("%w: token has no expiration claim", sdkauth.ErrInvalidToken)
		}
		return &sdkauth.TokenInfo{
			UserID:     subject,
			Expiration: expiration.Time,
			Extra: map[string]any{
				rawTokenExtraKey: token,
			},
		}, nil
	}
}

// forwardToken forwards the bearer token carried by the incoming MCP tool call to the outgoing gRPC context, so that
// downstream fulfillment-service calls are attributed to the calling user rather than a fixed service identity.
func forwardToken(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if req == nil || req.Extra == nil || req.Extra.TokenInfo == nil {
		return ctx
	}
	rawToken, ok := req.Extra.TokenInfo.Extra[rawTokenExtraKey].(string)
	if !ok || rawToken == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+rawToken)
}

// userAgent is the user agent string for the MCP server.
const userAgent = "fulfillment-mcp-server"

const shortHelp = `Starts the MCP server`

const longHelp = `
Starts the MCP server.

**Experimental.** This command exposes a subset of the fulfillment-service API as Model
Context Protocol tools, for use by AI agents. Its tool set and transport are subject to
change without notice.
`

const trustedTokenIssuersFlagHelp = `
_ISSUERS_ - Comma separated list of token issuers that are trusted to authenticate callers.
`

const caFileFlagHelp = `
_FILE|DIRECTORY_ - File or directory containing trusted CA certificates.
`
