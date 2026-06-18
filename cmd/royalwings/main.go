package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"
	"github.com/royaldevlopments/royalwings/internal/activity"
	"github.com/royaldevlopments/royalwings/internal/api"
	"github.com/royaldevlopments/royalwings/internal/backup"
	"github.com/royaldevlopments/royalwings/internal/config"
	"github.com/royaldevlopments/royalwings/internal/environment"
	"github.com/royaldevlopments/royalwings/internal/panel"
	"github.com/royaldevlopments/royalwings/internal/server"
	"github.com/royaldevlopments/royalwings/internal/sftp"
	"github.com/royaldevlopments/royalwings/internal/websocket"
)

func main() {
	configPath := flag.String("config", "/etc/royalwings/config.yml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if cfg.Debug {
		log.Println("Debug mode enabled")
	}

	if err := os.MkdirAll(cfg.Data, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	panelClient := panel.NewClient(cfg.Panel)

	dockerEnv, err := environment.NewDocker(
		cfg.Docker.Socket,
		cfg.Docker.Network,
		cfg.Data,
	)
	if err != nil {
		log.Fatalf("Failed to create docker client: %v", err)
	}
	defer dockerEnv.Close()

	serverManager := server.NewManager(dockerEnv, panelClient, cfg.Data)

	if err := serverManager.Start(); err != nil {
		log.Fatalf("Failed to start server manager: %v", err)
	}
	defer serverManager.Stop()

	activityLogger, err := activity.NewLogger(cfg.Activity.LogFile, panelClient)
	if err != nil {
		log.Fatalf("Failed to create activity logger: %v", err)
	}
	defer activityLogger.Close()

	backup.NewManager(
		cfg.Backups.Directory,
		cfg.Backups.WriteLimit,
		serverManager,
		panelClient,
	)

	apiServer := api.New(
		cfg.System.HTTP,
		cfg.Token,
		serverManager,
		panelClient,
	)

	wsHandler := websocket.NewHandler(cfg.Token, serverManager)

	httpMux := apiServer.GetEcho()
	httpMux.GET("/ws", func(c echo.Context) error {
		wsHandler.Handle(c.Response().Writer, c.Request())
		return nil
	})

	go func() {
		log.Printf("Starting API server on %s", cfg.System.HTTP)
		if err := apiServer.Start(); err != nil {
			log.Fatalf("API server failed: %v", err)
		}
	}()

	sftpServer, err := sftp.New(
		cfg.SFTP.Address,
		cfg.SFTP.Port,
		serverManager,
		panelClient,
		cfg.Data,
	)
	if err != nil {
		log.Fatalf("Failed to create SFTP server: %v", err)
	}

	go func() {
		if err := sftpServer.Start(); err != nil {
			log.Fatalf("SFTP server failed: %v", err)
		}
	}()

	log.Printf("Royal Wings started")
	log.Printf("  API:    %s", cfg.System.HTTP)
	log.Printf("  SFTP:   %s:%d", cfg.SFTP.Address, cfg.SFTP.Port)
	log.Printf("  Panel:  %s", cfg.Panel.URL)
	log.Printf("  Data:   %s", cfg.Data)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	sig := <-sigCh

	log.Printf("Received signal %v, shutting down...", sig)

	serverManager.Stop()
	apiServer.Stop()
	activityLogger.Close()
}
