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
	"strings"
	"syscall"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
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
	flags.StringVar(
		&runner.args.oauthAuthorizationServer,
		"oauth-authorization-server",
		"",
		oauthAuthorizationServerFlagHelp,
	)
	flags.StringVar(
		&runner.args.oauthResourceURL,
		"oauth-resource-url",
		"",
		oauthResourceURLFlagHelp,
	)
	return command
}

// runnerContext contains the data and logic needed to run the `start mcp-server` command.
type runnerContext struct {
	logger *slog.Logger
	flags  *pflag.FlagSet
	args   struct {
		trustedTokenIssuers      []string
		caFiles                  []string
		oauthAuthorizationServer string
		oauthResourceURL         string
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
	handler, err := NewHandler(ServerDeps{
		CatalogItemsClient: publicv1.NewClusterCatalogItemsClient(grpcClient),
		ClustersClient:     publicv1.NewClustersClient(grpcClient),
	}, jwtValidator, c.args.oauthAuthorizationServer, c.args.oauthResourceURL)
	if err != nil {
		return fmt.Errorf("failed to create MCP handler: %w", err)
	}

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

// oauthProtectedResourcePath is where the RFC 9728 protected-resource-metadata document is served, when OAuth
// discovery is configured. A spec-compliant MCP client that receives a 401 with this path in the WWW-Authenticate
// header's resource_metadata hint fetches it to learn which Authorization Server protects this resource, then
// drives a real interactive browser login on its own — no manual bearer-token configuration needed.
const oauthProtectedResourcePath = "/.well-known/oauth-protected-resource"

// NewHandler builds the composed HTTP handler for the MCP server: tool registration (newServer), the Streamable
// HTTP transport, bearer-token verification, and — when oauthAuthorizationServer/oauthResourceURL are both
// non-empty — RFC 9728 protected-resource-metadata discovery. Exported so callers can stand up the exact
// composition the command serves, without re-deriving the wiring.
//
// oauthAuthorizationServer is the Keycloak realm issuer to advertise as the protecting Authorization Server.
// oauthResourceURL is this MCP server's own canonical externally-reachable URL (the "resource" in RFC 9728/8707
// terms) — it can't be inferred from the listener's bind address alone, since a real deployment sits behind an
// ingress/gateway on a different host and port. The two must be set together, or not at all; NewHandler returns
// an error otherwise, since discovery can't be partially configured.
//
// When both are set, every request — not just ones to the metadata path — is routed through an http.ServeMux
// first. Per net/http's own documented behavior, ServeMux redirects requests with a non-canonical path (repeated
// slashes, "." or ".." segments) before they reach the streamable handler; a client behind a proxy that produces
// such a path against the MCP endpoint would previously have been served directly and will now see a redirect
// instead. Accepted as a narrow trade-off of enabling discovery, not a regression in bearer-token verification
// itself, which is otherwise identical either way.
func NewHandler(
	deps ServerDeps, validator auth.JwtValidator, oauthAuthorizationServer, oauthResourceURL string,
) (http.Handler, error) {
	if (oauthAuthorizationServer == "") != (oauthResourceURL == "") {
		return nil, errors.New(
			"'--oauth-authorization-server' and '--oauth-resource-url' must be set together, or not at all",
		)
	}
	// A trailing slash would otherwise double up against oauthProtectedResourcePath's own leading slash, and
	// would make the advertised "resource" identifier inconsistent with whatever exact string operators expect
	// Keycloak's issued-token audience/resource-indicator checks to match.
	oauthResourceURL = strings.TrimSuffix(oauthResourceURL, "/")
	server := newServer(deps)
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless: true,
		},
	)
	var resourceMetadataURL string
	if oauthResourceURL != "" {
		resourceMetadataURL = oauthResourceURL + oauthProtectedResourcePath
	}
	authenticatedHandler := sdkauth.RequireBearerToken(newTokenVerifier(validator), &sdkauth.RequireBearerTokenOptions{
		// Matches the JWT validator's own expiration leeway, so the SDK's independent expiration check
		// doesn't reject tokens the validator itself still considers valid.
		ClockSkew:           tokenExpirationLeeway,
		ResourceMetadataURL: resourceMetadataURL,
	})(streamableHandler)
	if oauthAuthorizationServer == "" {
		return authenticatedHandler, nil
	}
	// The metadata document itself must never require the very bearer token clients are trying to discover how to
	// obtain, so it's mounted unauthenticated, on a mux alongside (not wrapped by) the authenticated MCP endpoint.
	mux := http.NewServeMux()
	mux.Handle(oauthProtectedResourcePath, sdkauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:             oauthResourceURL,
		AuthorizationServers: []string{oauthAuthorizationServer},
	}))
	mux.Handle("/", authenticatedHandler)
	return mux, nil
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

const oauthAuthorizationServerFlagHelp = `
_URL_ - Issuer URL of the OAuth authorization server (Keycloak realm) that protects this MCP server, advertised via
RFC 9728 protected-resource-metadata discovery so spec-compliant MCP clients (e.g. Cursor, Claude Desktop) can drive
a real interactive login instead of requiring a manually configured bearer token. Must be set together with
{{ bt }}--oauth-resource-url{{ bt }}, or not at all.
`

const oauthResourceURLFlagHelp = `
_URL_ - This MCP server's own canonical externally-reachable URL, advertised as the "resource" in the protected-
resource-metadata document. Must be set together with {{ bt }}--oauth-authorization-server{{ bt }}, or not at all.
`
