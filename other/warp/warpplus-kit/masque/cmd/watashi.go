// watashi v1.0.0-masque: headless engine mode for the Watashi panel.
// Registers (or loads) a WARP MASQUE identity automatically and serves the
// existing SOCKS entry point, so the panel can treat MASQUE like its other
// engine modes: one binary, one config dir, no interactive prompts.
package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Diniboy1123/usque/api"
	"github.com/Diniboy1123/usque/config"
	"github.com/Diniboy1123/usque/internal"
	"github.com/spf13/cobra"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

var watashiCmd = &cobra.Command{
	Use:   "watashi",
	Short: "Watashi engine mode: auto-register and serve SOCKS5 over MASQUE",
	Run: func(cmd *cobra.Command, args []string) {
		configPath, err := cmd.Flags().GetString("config")
		if err != nil || configPath == "" {
			log.Fatalf("watashi: config path is required")
		}

		bindAddress, err := cmd.Flags().GetString("bind")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		port, err := cmd.Flags().GetString("port")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		connectPort, err := cmd.Flags().GetInt("connect-port")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		useIPv6, err := cmd.Flags().GetBool("ipv6")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		sni, err := cmd.Flags().GetString("sni-address")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		mtu, err := cmd.Flags().GetInt("mtu")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		dnsServers, err := cmd.Flags().GetStringArray("dns")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		keepalivePeriod, err := cmd.Flags().GetDuration("keepalive-period")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		reconnectDelay, err := cmd.Flags().GetDuration("reconnect-delay")
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		err = config.LoadConfig(configPath)
		if err != nil {
			log.Printf("watashi: no MASQUE identity yet, registering a new one")
			registerQuiet(configPath)
			if err := config.LoadConfig(configPath); err != nil {
				log.Fatalf("watashi: identity saved but could not be loaded: %v", err)
			}
		}
		log.Printf("watashi: MASQUE identity loaded (id %s)", config.AppConfig.ID)

		privKey, err := config.AppConfig.GetEcPrivateKey()
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		peerPubKey, err := config.AppConfig.GetEcEndpointPublicKey()
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		cert, err := internal.GenerateCert(privKey, &privKey.PublicKey)
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		tlsConfig, err := api.PrepareTlsConfig(privKey, peerPubKey, cert, sni, false)
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		endpoint, err := config.SelectEndpointFromConfig(false, useIPv6, connectPort)
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		var localAddresses []netip.Addr
		v4, err := netip.ParseAddr(config.AppConfig.IPv4)
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		localAddresses = append(localAddresses, v4)
		v6, err := netip.ParseAddr(config.AppConfig.IPv6)
		if err == nil {
			localAddresses = append(localAddresses, v6)
		}

		var dnsAddrs []netip.Addr
		for _, dns := range dnsServers {
			addr, err := netip.ParseAddr(dns)
			if err != nil {
				log.Fatalf("watashi: %v", err)
			}
			dnsAddrs = append(dnsAddrs, addr)
		}

		tunDev, tunNet, err := netstack.CreateNetTUN(localAddresses, dnsAddrs, mtu)
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}
		defer func() { _ = tunDev.Close() }()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go api.MaintainTunnel(ctx, api.MaintainTunnelConfig{
			TLSConfig:         tlsConfig,
			KeepalivePeriod:   keepalivePeriod,
			InitialPacketSize: 0,
			Endpoint:          endpoint,
			Device:            api.NewNetstackAdapter(tunDev),
			MTU:               mtu,
			ReconnectDelay:    reconnectDelay,
			AlwaysReconnect:   true,
			UseHTTP2:          false,
		})

		resolver := &internal.TunnelDNSResolver{
			DNSAddrs:      dnsAddrs,
			Timeout:       2 * time.Second,
			UseOSResolver: false,
		}
		resolver.TunNet = tunNet

		server, err := internal.NewSOCKS5Server(internal.SOCKS5Config{
			Addr:     net.JoinHostPort(bindAddress, port),
			Resolver: resolver,
			TunNet:   tunNet,
			Logger:   log.New(internal.NewTZStampWriter(os.Stderr), "masque: ", 0),
		})
		if err != nil {
			log.Fatalf("watashi: %v", err)
		}

		log.Printf("watashi: SOCKS5 proxy listening on %s:%s", bindAddress, port)

		go func() {
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig
			log.Printf("watashi: shutting down")
			cancel()
			os.Exit(0)
		}()

		if err := server.Start(); err != nil {
			log.Fatalf("watashi: socks proxy exited: %v", err)
		}
	},
}

// registerQuiet runs the register+enroll chain without any prompt: the panel
// calls this from its own scripts, so TOS is accepted on the operator's behalf
// and no overwrite question exists (the caller decided the path).
func registerQuiet(configPath string) {
	accountData, err := api.Register(internal.DefaultModel, internal.DefaultLocale, "", true)
	if err != nil {
		log.Fatalf("watashi: register failed: %v", err)
	}

	privKey, pubKey, err := internal.GenerateEcKeyPair()
	if err != nil {
		log.Fatalf("watashi: %v", err)
	}

	updatedAccountData, err := api.EnrollKey(accountData.ID, accountData.Token, pubKey, "watashi-engine")
	if err != nil {
		log.Fatalf("watashi: enroll failed: %v", err)
	}

	config.AppConfig = config.Config{
		PrivateKey:     base64.StdEncoding.EncodeToString(privKey),
		EndpointV4:     updatedAccountData.Config.Peers[0].Endpoint.V4[:len(updatedAccountData.Config.Peers[0].Endpoint.V4)-2],
		EndpointV6:     updatedAccountData.Config.Peers[0].Endpoint.V6[1 : len(updatedAccountData.Config.Peers[0].Endpoint.V6)-3],
		EndpointH2V4:   config.DefaultEndpointH2V4,
		EndpointH2V6:   config.DefaultEndpointH2V6,
		EndpointPubKey: updatedAccountData.Config.Peers[0].PublicKey,
		ID:             updatedAccountData.ID,
		AccessToken:    accountData.Token,
		IPv4:           updatedAccountData.Config.Interface.Addresses.V4,
		IPv6:           updatedAccountData.Config.Interface.Addresses.V6,
	}

	if err := config.AppConfig.SaveConfig(configPath); err != nil {
		log.Fatalf("watashi: %v", err)
	}

	log.Printf("watashi: new MASQUE identity saved to %s", configPath)
	fmt.Println("watashi: register done")
}

func init() {
	watashiCmd.Flags().StringP("config", "c", "", "path to the MASQUE identity config")
	watashiCmd.Flags().StringP("bind", "b", "127.0.0.1", "Address to bind the SOCKS proxy to")
	watashiCmd.Flags().StringP("port", "p", "3000", "Port to listen on for SOCKS proxy")
	watashiCmd.Flags().IntP("connect-port", "P", 443, "Used port for MASQUE connection")
	watashiCmd.Flags().BoolP("ipv6", "6", false, "Use IPv6 for MASQUE connection")
	watashiCmd.Flags().StringP("sni-address", "s", internal.ConnectSNI, "SNI address to use for MASQUE connection")
	watashiCmd.Flags().IntP("mtu", "m", 1280, "MTU for MASQUE connection")
	watashiCmd.Flags().StringArrayP("dns", "d", []string{"1.1.1.1", "1.0.0.1"}, "DNS servers for the tunnel stack")
	watashiCmd.Flags().DurationP("keepalive-period", "k", 30*time.Second, "Keepalive period for MASQUE connection")
	watashiCmd.Flags().DurationP("reconnect-delay", "r", 1*time.Second, "Delay between reconnect attempts")
	rootCmd.AddCommand(watashiCmd)
}
