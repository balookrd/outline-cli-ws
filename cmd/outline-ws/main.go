package main

import (
	"fmt"
	"os"
	"path/filepath"

	"outline-cli-ws/internal/config"
	"outline-cli-ws/internal/manager"

	"github.com/spf13/cobra"
)

var (
	configDir string
	cfg       *config.GlobalConfig
)

var rootCmd = &cobra.Command{
	Use:   "outline-ws",
	Short: "Outline client with WebSocket support",
	Long: `Outline client for Linux with support for Shadowsocks-over-WebSocket.
Supports both standard ss:// keys and WebSocket YAML format.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		cfg, err = config.LoadGlobalConfig(configDir)
		return err
	},
}

var addCmd = &cobra.Command{
	Use:   "add [key-or-file] [name]",
	Short: "Add a new server",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		name := "server"
		if len(args) > 1 {
			name = args[1]
		}

		server, err := config.ParseKey(key, name)
		if err != nil {
			return fmt.Errorf("failed to parse key: %w", err)
		}

		cfg.Servers = append(cfg.Servers, server)
		return cfg.Save()
	},
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all servers",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(cfg.Servers) == 0 {
			fmt.Println("No servers configured")
			return nil
		}

		for i, server := range cfg.Servers {
			active := " "
			if server.ID == cfg.ActiveID {
				active = "*"
			}

			wsInfo := ""
			if server.WebSocket {
				wsInfo = fmt.Sprintf(" (WS%s)", map[bool]string{true: "+TLS", false: ""}[server.UseTLS])
			}

			fmt.Printf("%s[%d] %s%s - %s:%d\n",
				active, i+1, server.Name, wsInfo, server.Server, server.Port)
		}
		return nil
	},
}

var connectCmd = &cobra.Command{
	Use:   "connect [name-or-index]",
	Short: "Connect to a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var server *config.ServerConfig

		// Поиск сервера по индексу или имени
		for i, s := range cfg.Servers {
			if fmt.Sprintf("%d", i+1) == args[0] || s.Name == args[0] {
				server = s
				break
			}
		}

		if server == nil {
			return fmt.Errorf("server not found: %s", args[0])
		}

		cfg.ActiveID = server.ID
		cfg.Save()

		vpnManager := manager.NewVPNManager(cfg)
		return vpnManager.Connect(server)
	},
}

var disconnectCmd = &cobra.Command{
	Use:   "disconnect",
	Short: "Disconnect current server",
	RunE: func(cmd *cobra.Command, args []string) error {
		vpnManager := manager.NewVPNManager(cfg)
		return vpnManager.Disconnect()
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show connection status",
	RunE: func(cmd *cobra.Command, args []string) error {
		vpnManager := manager.NewVPNManager(cfg)
		status := vpnManager.GetStatus()

		fmt.Printf("Status: %s\n", status.State)
		if status.Server != nil {
			fmt.Printf("Server: %s (%s:%d)\n", status.Server.Name, status.Server.Server, status.Server.Port)
			fmt.Printf("Traffic: ↑ %d ↓ %d\n", status.Upload, status.Download)
		}
		return nil
	},
}

var removeCmd = &cobra.Command{
	Use:   "remove [name-or-index]",
	Short: "Remove a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		newServers := []*config.ServerConfig{}
		removed := false

		for i, s := range cfg.Servers {
			if fmt.Sprintf("%d", i+1) == args[0] || s.Name == args[0] {
				removed = true
				if s.ID == cfg.ActiveID {
					cfg.ActiveID = ""
				}
				continue
			}
			newServers = append(newServers, s)
		}

		if !removed {
			return fmt.Errorf("server not found: %s", args[0])
		}

		cfg.Servers = newServers
		return cfg.Save()
	},
}

func init() {
	home, _ := os.UserHomeDir()
	rootCmd.PersistentFlags().StringVar(&configDir, "config",
		filepath.Join(home, ".config", "outline-ws"),
		"config directory")

	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(connectCmd)
	rootCmd.AddCommand(disconnectCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(removeCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
