// Package deploy embeds installation files served by the hub.
package deploy

import _ "embed"

// AgentUnit is the systemd unit for the host agent.
//
//go:embed systemd/monitor-agent.service
var AgentUnit string

// GuestUnit is the systemd unit for the guest agent. The install script
// replaces {{SUPPLEMENTARY}} with "SupplementaryGroups=docker" when the
// docker group exists.
//
//go:embed systemd/monitor-agent-guest.service
var GuestUnit string

// InstallAgentTmpl is the text/template of the agent install script.
//
//go:embed install-agent.sh.tmpl
var InstallAgentTmpl string
