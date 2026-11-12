// Package deploy embeds installation files served by the hub.
package deploy

import _ "embed"

// AgentUnit is the systemd unit for the host agent.
//
//go:embed systemd/monitor-agent.service
var AgentUnit string

// InstallAgentTmpl is the text/template of the agent install script.
//
//go:embed install-agent.sh.tmpl
var InstallAgentTmpl string
