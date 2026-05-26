package agent

import (
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
)

// MCPServerConfig mirrors config.MCPServer.
type MCPServerConfig struct {
	Name     string
	Endpoint string // "command:arg1:arg2" or "http://..."
	Enabled  bool
}

// LoadMCPTools creates ADK Toolsets from configured MCP servers.
// Returns toolsets that should be added to the agent's tool list.
func LoadMCPTools(servers []MCPServerConfig) []tool.Toolset {
	var toolsets []tool.Toolset

	for _, srv := range servers {
		if !srv.Enabled {
			continue
		}

		var transport mcp.Transport

		if strings.HasPrefix(srv.Endpoint, "http://") || strings.HasPrefix(srv.Endpoint, "https://") {
			// SSE transport for HTTP-based MCP servers
			transport = &mcp.SSEClientTransport{Endpoint: srv.Endpoint}
		} else {
			// Command transport for stdio-based MCP servers
			parts := strings.Fields(srv.Endpoint)
			if len(parts) == 0 {
				log.Printf("MCP server %q: empty endpoint", srv.Name)
				continue
			}
			transport = &mcp.CommandTransport{
				Command: exec.Command(parts[0], parts[1:]...),
			}
		}

		ts, err := mcptoolset.New(mcptoolset.Config{
			Transport: transport,
		})
		if err != nil {
			log.Printf("MCP server %q: %v", srv.Name, err)
			continue
		}

		log.Printf("Loaded MCP toolset: %s (%s)", srv.Name, srv.Endpoint)
		toolsets = append(toolsets, ts)
	}

	return toolsets
}

// MCPToolsAsTools converts toolsets to a flat tool list for the ADK agent.
func MCPToolsAsTools(toolsets []tool.Toolset) ([]tool.Tool, error) {
	var tools []tool.Tool
	for _, ts := range toolsets {
		tsTools, err := ts.Tools(nil)
		if err != nil {
			return nil, fmt.Errorf("list tools from toolset %s: %w", ts.Name(), err)
		}
		tools = append(tools, tsTools...)
	}
	return tools, nil
}
