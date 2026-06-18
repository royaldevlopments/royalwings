package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/royaldevlopments/royalwings/internal/panel"
	"github.com/royaldevlopments/royalwings/internal/server"
)

type Server struct {
	echo     *echo.Echo
	addr     string
	token    string
	manager  *server.Manager
	panel    *panel.Client
}

func New(addr, token string, manager *server.Manager, panel *panel.Client) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	s := &Server{
		echo:    e,
		addr:    addr,
		token:   token,
		manager: manager,
		panel:   panel,
	}

	s.setupMiddleware()
	s.setupRoutes()

	return s
}

func (s *Server) setupMiddleware() {
	s.echo.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
		Format: "method=${method}, uri=${uri}, status=${status}, latency=${latency_human}\n",
	}))

	s.echo.Use(middleware.Recover())

	s.echo.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Path() == "/api/v1/health" {
				return next(c)
			}
			auth := c.Request().Header.Get("Authorization")
			if auth != "Bearer "+s.token {
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "unauthorized",
				})
			}
			return next(c)
		}
	})
}

func (s *Server) setupRoutes() {
	api := s.echo.Group("/api/v1")

	api.GET("/health", s.handleHealth)

	servers := api.Group("/servers")
	servers.GET("", s.handleListServers)
	servers.GET("/:uuid", s.handleGetServer)
	servers.POST("/:uuid/start", s.handleStartServer)
	servers.POST("/:uuid/stop", s.handleStopServer)
	servers.POST("/:uuid/restart", s.handleRestartServer)
	servers.POST("/:uuid/kill", s.handleKillServer)
	servers.POST("/:uuid/command", s.handleSendCommand)
	servers.GET("/:uuid/logs", s.handleGetLogs)
	servers.GET("/:uuid/state", s.handleGetState)
}

func (s *Server) Start() error {
	return s.echo.Start(s.addr)
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.echo.Shutdown(ctx)
}

func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) handleListServers(c echo.Context) error {
	servers := s.manager.ListServers()
	result := make([]map[string]interface{}, 0, len(servers))

	for _, srv := range servers {
		result = append(result, map[string]interface{}{
			"uuid":       srv.UUID,
			"name":       srv.Name,
			"state":      srv.GetState(),
			"container_id": srv.ContainerID,
		})
	}

	return c.JSON(http.StatusOK, result)
}

func (s *Server) handleGetServer(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "server not found",
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"uuid":         srv.UUID,
		"name":         srv.Name,
		"state":        srv.GetState(),
		"container_id": srv.ContainerID,
	})
}

func (s *Server) handleStartServer(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if srv.Config.Suspended {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "server is suspended"})
	}

	if err := srv.Start(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleStopServer(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleRestartServer(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if srv.Config.Suspended {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "server is suspended"})
	}

	if err := srv.Restart(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "restarted"})
}

func (s *Server) handleKillServer(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Kill(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "killed"})
}

func (s *Server) handleSendCommand(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	var req struct {
		Command string `json:"command"`
	}

	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.SendCommand(ctx, req.Command); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "sent"})
}

func (s *Server) handleGetLogs(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	tail := c.QueryParam("tail")
	if tail == "" {
		tail = "100"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logs, err := srv.GetLogs(ctx, tail)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"logs": logs,
	})
}

func (s *Server) GetEcho() *echo.Echo {
	return s.echo
}

func (s *Server) handleGetState(c echo.Context) error {
	uuid := c.Param("uuid")
	srv, ok := s.manager.GetServer(uuid)
	if !ok {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "server not found"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"uuid":  srv.UUID,
		"state": srv.GetState(),
	})
}

func getContentType(data []byte) string {
	if len(data) > 0 && data[0] == '{' {
		return "application/json"
	}
	return http.DetectContentType(data)
}
