// Command mcp-oauth-demo-client is a reference OAuth client for the OSAC Deployment MCP PoC (OSAC-4388).
//
// It proves the RFC 9728 discovery + OAuth 2.0 Authorization Code + PKCE handshake against fulfillment-service's
// `start mcp-server` command works end to end, without requiring a full AI IDE's MCP settings to exercise it: it
// discovers Keycloak as the Authorization Server, drives a real interactive browser login, then calls the four MCP
// tools (list_catalog_items -> describe_catalog_item -> create_cluster_from_catalog_item -> get_cluster_status) in
// sequence, printing what happened.
//
// It's modeled directly on the MCP Go SDK's own official example
// (examples/auth/client/main.go in github.com/modelcontextprotocol/go-sdk), specialized to a fixed "/callback" path
// matching the redirect URI registered for -client-id in Keycloak's bootstrap realm (see osac-installer's
// charts/osac-infra/files/realm.json).
//
// This program has no dependency on any fulfillment-service internal package — it only speaks the MCP wire
// protocol — so it lives standalone here rather than inside that module.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

var (
	serverURL = flag.String(
		"server-url", "http://localhost:8001",
		"URL of the mcp-server instance to connect to.",
	)
	callbackPort = flag.Int(
		"callback-port", 8091,
		"Port for the local HTTP server that receives the OAuth redirect. Must match the port in the redirect URI "+
			"registered for -client-id in Keycloak.",
	)
	clientID = flag.String(
		"client-id", "osac-mcp-client",
		"OAuth client ID pre-registered in Keycloak (see osac-installer's realm.json).",
	)
	issuer = flag.String(
		"issuer", "",
		"Optional: pin the expected Keycloak realm issuer URL. If set, authorization fails if the "+
			"server-advertised issuer doesn't match — recommended once you know the real issuer, to guard "+
			"against a misconfigured or spoofed Authorization Server.",
	)
	caFile = flag.String(
		"ca-file", "",
		"Optional: path to a PEM file of additional CA certificates to trust, appended to the system pool. Needed "+
			"against a dev cluster (e.g. kind) whose Keycloak/mcp-server TLS certs are signed by a cluster-local "+
			"CA the host OS doesn't already trust — see osac-installer's ca-bundle ConfigMap.",
	)
	catalogItem = flag.String(
		"catalog-item", "",
		"id or name of the catalog item to create a demo cluster from. If empty, the first item returned by "+
			"list_catalog_items is used.",
	)
	clusterName = flag.String(
		"cluster-name", "",
		`Name for the demo cluster. Defaults to "mcp-oauth-demo-<unix timestamp>".`,
	)
	pollCount = flag.Int(
		"poll", 3,
		"Number of extra times to call get_cluster_status after creation, to show state progress over time. "+
			"0 means only the initial status check runs.",
	)
	pollInterval = flag.Duration(
		"poll-interval", 5*time.Second,
		"Delay between status polls.",
	)
	fieldOverrides fieldOverrideFlag
)

func init() {
	flag.Var(
		&fieldOverrides, "set",
		"key=value field override for create_cluster_from_catalog_item (repeatable), matching the catalog "+
			"item's field_definitions paths from describe_catalog_item.",
	)
}

// fieldOverrideFlag collects repeated -set key=value flags into a slice, matching flag.Value's contract for
// multi-value flags (the standard library's flag package has no built-in slice flag type).
type fieldOverrideFlag []string

func (f *fieldOverrideFlag) String() string { return strings.Join(*f, ",") }

func (f *fieldOverrideFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// These mirror the JSON shapes of fulfillment-service/internal/cmd/service/start/mcpserver's tool output types
// (tool_catalog_items.go, tool_clusters.go). Duplicated here deliberately, not imported: this program speaks only
// the MCP wire protocol, matching what any other MCP client (this program is a stand-in for a real one, e.g.
// Cursor or Claude Desktop) would have to do — there's no shared Go type to import across the module boundary,
// and there shouldn't be; the JSON schema each tool advertises is the actual contract.

type catalogItemSummary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type listCatalogItemsOutput struct {
	Items []catalogItemSummary `json:"items"`
}

type fieldDefinitionSummary struct {
	Path         string `json:"path"`
	DisplayName  string `json:"display_name"`
	Editable     bool   `json:"editable"`
	DefaultValue string `json:"default_value,omitempty"`
}

type describeCatalogItemOutput struct {
	ID               string                   `json:"id"`
	Title            string                   `json:"title"`
	Description      string                   `json:"description"`
	FieldDefinitions []fieldDefinitionSummary `json:"field_definitions"`
}

type createClusterOutput struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type conditionSummary struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type getClusterStatusOutput struct {
	ID         string             `json:"id"`
	State      string             `json:"state"`
	Conditions []conditionSummary `json:"conditions"`
	APIURL     string             `json:"api_url,omitempty"`
	ConsoleURL string             `json:"console_url,omitempty"`
}

// codeReceiver is a loopback HTTP server that catches Keycloak's redirect back to this program after the user
// logs in, and hands the authorization code/state to whichever goroutine is waiting for it. Directly modeled on
// the SDK's own example client, specialized to a fixed "/callback" path (the SDK example uses a bare catch-all
// "/") because the Keycloak client this program authenticates as has an exact-match redirect URI registered, not
// a wildcard — see the package doc comment.
type codeReceiver struct {
	authChan chan *auth.AuthorizationResult
	errChan  chan error
	server   *http.Server
}

func (r *codeReceiver) serve(listener net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, req *http.Request) {
		result := &auth.AuthorizationResult{
			Code:  req.URL.Query().Get("code"),
			State: req.URL.Query().Get("state"),
			Iss:   req.URL.Query().Get("iss"),
		}
		// Non-blocking: getAuthorizationCode only ever receives once. A browser reload of this static
		// confirmation page re-triggers this handler with nothing left to read a second send, which would
		// otherwise block this handler goroutine forever on an unbuffered (or even singly-buffered) channel.
		select {
		case r.authChan <- result:
		default:
		}
		_, _ = fmt.Fprintln(w, "Login successful \u2014 you can close this window and return to the terminal.")
	})
	r.server = &http.Server{Handler: mux}
	if err := r.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		r.errChan <- err
	}
}

func (r *codeReceiver) getAuthorizationCode(
	ctx context.Context, args *auth.AuthorizationArgs,
) (*auth.AuthorizationResult, error) {
	fmt.Println("Opening your browser to log in via Keycloak...")
	fmt.Printf("If it doesn't open automatically, visit this URL:\n%s\n\n", args.URL)
	openBrowser(args.URL)
	select {
	case result := <-r.authChan:
		return result, nil
	case err := <-r.errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *codeReceiver) close() {
	if r.server != nil {
		_ = r.server.Close()
	}
}

// openBrowser makes a best-effort attempt to open url in the user's default browser. Failure is non-fatal — the
// URL is always printed too, so the demo still works with a manual copy/paste.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("Could not auto-open a browser (%v) \u2014 use the URL above instead.", err)
	}
}

// callTool calls an MCP tool and decodes its structured output into T. A non-nil error here always means a
// transport/protocol-level failure (or a tool-level failure, surfaced via CallToolResult.IsError) — tool handlers
// on the server side report their own business-logic errors this way, not as protocol errors, per the MCP spec's
// guidance that handlers should let the calling agent see and react to errors rather than aborting the session.
func callTool[T any](ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (T, error) {
	var zero T
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return zero, fmt.Errorf("calling %q: %w", name, err)
	}
	if result.IsError {
		return zero, fmt.Errorf("%q returned an error: %s", name, resultText(result))
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return zero, fmt.Errorf("marshaling %q's structured content: %w", name, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("unmarshaling %q's structured content: %w", name, err)
	}
	return out, nil
}

// resultText concatenates a tool result's unstructured text content, for printing alongside a tool-level error.
func resultText(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// newHTTPClient builds the *http.Client used for every request this program makes to the Authorization Server and
// mcp-server: protected-resource/authorization-server metadata discovery, the token exchange, and the MCP wire
// protocol itself. Returns nil (letting SDK callers fall back to their own default) when -ca-file is unset, so the
// common case — a real CA already trusted by the host OS — needs no extra plumbing.
func newHTTPClient() *http.Client {
	if *caFile == "" {
		return nil
	}
	pem, err := os.ReadFile(*caFile)
	if err != nil {
		log.Fatalf("failed to read -ca-file %q: %v", *caFile, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		log.Fatalf("no certificates found in -ca-file %q", *caFile)
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool}, //nolint:gosec // pool always includes the system roots too
		},
	}
}

func main() {
	flag.Parse()
	httpClient := newHTTPClient()

	receiver := &codeReceiver{
		// Buffered by 1: getAuthorizationCode only ever receives once, but a browser reload of the static
		// "login successful" confirmation page re-triggers the handler and would otherwise block that second
		// handler goroutine forever on an unbuffered channel nobody reads from again.
		authChan: make(chan *auth.AuthorizationResult, 1),
		errChan:  make(chan error, 1),
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *callbackPort))
	if err != nil {
		log.Fatalf("failed to listen on the OAuth callback port %d: %v", *callbackPort, err)
	}
	go receiver.serve(listener)
	defer receiver.close()

	authHandler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		RedirectURL:              fmt.Sprintf("http://localhost:%d/callback", *callbackPort),
		AuthorizationCodeFetcher: receiver.getAuthorizationCode,
		PreregisteredClient: &oauthex.ClientCredentials{
			ClientID: *clientID,
			Issuer:   *issuer,
		},
		Client: httpClient,
	})
	if err != nil {
		log.Fatalf("failed to create the OAuth handler: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "osac-mcp-oauth-demo-client", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:     *serverURL,
		OAuthHandler: authHandler,
		HTTPClient:   httpClient,
	}

	ctx := context.Background()
	log.Printf("Connecting to %s ...", *serverURL)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	log.Println("Connected and authenticated \u2014 the browser login above succeeded.")

	items, err := callTool[listCatalogItemsOutput](ctx, session, "list_catalog_items", nil)
	if err != nil {
		log.Fatalf("list_catalog_items: %v", err)
	}
	if len(items.Items) == 0 {
		log.Fatal("no catalog items are published on this fulfillment-service instance \u2014 nothing to demo")
	}
	log.Printf("Found %d catalog item(s):", len(items.Items))
	for _, item := range items.Items {
		log.Printf("  - %s: %s \u2014 %s", item.ID, item.Title, item.Description)
	}

	ref := *catalogItem
	if ref == "" {
		ref = items.Items[0].ID
	}
	described, err := callTool[describeCatalogItemOutput](ctx, session, "describe_catalog_item", map[string]any{
		"id": ref,
	})
	if err != nil {
		log.Fatalf("describe_catalog_item(%q): %v", ref, err)
	}
	log.Printf("Describing %q (%s):", described.Title, described.ID)
	for _, field := range described.FieldDefinitions {
		log.Printf("  - %s (%s) editable=%v default=%q", field.Path, field.DisplayName, field.Editable, field.DefaultValue)
	}

	name := *clusterName
	if name == "" {
		name = fmt.Sprintf("mcp-oauth-demo-%d", time.Now().Unix())
	}
	createArgs := map[string]any{"name": name, "catalog_item": described.ID}
	if len(fieldOverrides) > 0 {
		createArgs["set"] = []string(fieldOverrides)
	}
	log.Printf("Creating cluster %q from %q ...", name, described.ID)
	created, err := callTool[createClusterOutput](ctx, session, "create_cluster_from_catalog_item", createArgs)
	if err != nil {
		log.Fatalf("create_cluster_from_catalog_item: %v", err)
	}
	log.Printf("Created cluster %s (state=%s)", created.ID, created.State)

	for i := 0; i <= *pollCount; i++ {
		status, err := callTool[getClusterStatusOutput](ctx, session, "get_cluster_status", map[string]any{
			"id": created.ID,
		})
		if err != nil {
			log.Fatalf("get_cluster_status: %v", err)
		}
		log.Printf("Status: state=%s", status.State)
		for _, condition := range status.Conditions {
			log.Printf("  - %s=%s: %s", condition.Type, condition.Status, condition.Message)
		}
		if status.APIURL != "" {
			log.Printf("  api_url=%s", status.APIURL)
		}
		if status.ConsoleURL != "" {
			log.Printf("  console_url=%s", status.ConsoleURL)
		}
		if i < *pollCount {
			time.Sleep(*pollInterval)
		}
	}

	log.Printf(
		"Demo complete. Cluster %q was NOT deleted automatically \u2014 clean it up (osac CLI or console) if this "+
			"was just a demo run.",
		name,
	)
}
