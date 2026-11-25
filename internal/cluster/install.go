package cluster

import (
	"fmt"
	"strings"
)

// Install holds ready-to-paste ways to start an agent for one node.
type Install struct {
	Server      string `json:"server"`
	Node        string `json:"node"`
	Fingerprint string `json:"fingerprint"`
	// Shell starts the agent in the foreground (Linux, macOS, BSD).
	Shell string `json:"shell"`
	// Systemd installs it as a service on Linux.
	Systemd string `json:"systemd"`
	// Windows starts it from PowerShell, as a service.
	Windows string `json:"windows"`
}

// InstallCommands renders the install commands. The token goes into a file
// readable only by its owner, never onto a command line.
func InstallCommands(server, node, token, fingerprint string) Install {
	args := fmt.Sprintf("--server %s --node %s --ca-fingerprint %s", shq(server), shq(node), fingerprint)
	shell := strings.Join([]string{
		"install -d -m 0700 ~/.unagikuma",
		"printf '%s\\n' " + shq(token) + " > ~/.unagikuma/agent.token",
		"chmod 0600 ~/.unagikuma/agent.token",
		"unagikuma agent " + args + " --token-file ~/.unagikuma/agent.token",
	}, "\n")
	systemd := strings.Join([]string{
		"sudo install -d -m 0700 /etc/unagikuma",
		"printf '%s\\n' " + shq(token) + " | sudo tee /etc/unagikuma/agent.token >/dev/null",
		"sudo chmod 0600 /etc/unagikuma/agent.token",
		"sudo tee /etc/systemd/system/unagikuma-agent.service >/dev/null <<'EOF'",
		"[Unit]",
		"Description=unagikuma probe agent " + node,
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"ExecStart=/usr/local/bin/unagikuma agent " + args + " --token-file ${CREDENTIALS_DIRECTORY}/agent.token",
		"LoadCredential=agent.token:/etc/unagikuma/agent.token",
		"DynamicUser=yes",
		"AmbientCapabilities=CAP_NET_RAW",
		"Restart=always",
		"RestartSec=5",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"EOF",
		"sudo systemctl daemon-reload && sudo systemctl enable --now unagikuma-agent",
	}, "\n")
	// An Administrator PowerShell, in the folder of unagikuma.exe.
	windows := strings.Join([]string{
		`New-Item -ItemType Directory -Force "$env:ProgramData\unagikuma" | Out-Null`,
		`Set-Content -NoNewline -Path "$env:ProgramData\unagikuma\agent.token" -Value ` + psq(token),
		`icacls "$env:ProgramData\unagikuma\agent.token" /inheritance:r /grant:r "SYSTEM:R" "Administrators:R" | Out-Null`,
		`.\unagikuma.exe service install --name unagikuma-agent -- agent --server ` + psq(server) + ` --node ` + psq(node) +
			` --ca-fingerprint ` + fingerprint + ` --token-file "$env:ProgramData\unagikuma\agent.token" --log-file "$env:ProgramData\unagikuma\agent.log"`,
	}, "\n")
	return Install{Server: server, Node: node, Fingerprint: fingerprint, Shell: shell, Systemd: systemd, Windows: windows}
}

// shq quotes for POSIX shells.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psq quotes for PowerShell.
func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
