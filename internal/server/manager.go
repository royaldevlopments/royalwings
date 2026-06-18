package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"io"

	"github.com/royaldevlopments/royalwings/internal/environment"
	"github.com/royaldevlopments/royalwings/internal/panel"
)

type State string

const (
	StateOffline   State = "offline"
	StateRunning   State = "running"
	StateStarting  State = "starting"
	StateStopping  State = "stopping"
	StateInstalling State = "installing"
	StateSuspended State = "suspended"
	StateRestoring State = "restoring_from_backup"
)

type Server struct {
	mu         sync.RWMutex
	UUID       string
	InternalID int
	Name       string
	Config     *panel.ServerConfiguration
	State      State
	ContainerID string
	docker     *environment.Docker
	panel      *panel.Client
	dataDir    string
	crashCount int
	lastCrash  time.Time
}

type Manager struct {
	mu       sync.RWMutex
	servers  map[string]*Server
	docker   *environment.Docker
	panel    *panel.Client
	dataDir  string
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewManager(docker *environment.Docker, panel *panel.Client, dataDir string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		servers:  make(map[string]*Server),
		docker:   docker,
		panel:    panel,
		dataDir:  dataDir,
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (m *Manager) Start() error {
	if err := os.MkdirAll(filepath.Join(m.dataDir, "servers"), 0755); err != nil {
		return fmt.Errorf("failed to create servers directory: %w", err)
	}

	if err := m.docker.EnsureNetwork(m.ctx); err != nil {
		return fmt.Errorf("failed to ensure docker network: %w", err)
	}

	if err := m.panel.ResetServerStates(); err != nil {
		log.Printf("Warning: failed to reset server states: %v", err)
	}

	servers, err := m.panel.GetServers()
	if err != nil {
		return fmt.Errorf("failed to fetch servers from panel: %w", err)
	}

	for _, srv := range servers.Data {
		if err := m.AddServer(&srv); err != nil {
			log.Printf("Warning: failed to add server %s: %v", srv.UUID, err)
		}
	}

	log.Printf("Loaded %d servers from panel", len(m.servers))

	go m.crashLoop()

	return nil
}

func (m *Manager) Stop() {
	m.cancel()

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, srv := range m.servers {
		if srv.State == StateRunning || srv.State == StateStarting {
			log.Printf("Stopping server %s...", srv.UUID)
			if err := m.docker.StopContainer(context.Background(), srv.ContainerID, nil); err != nil {
				log.Printf("Warning: failed to stop server %s: %v", srv.UUID, err)
			}
		}
	}
}

func (m *Manager) AddServer(cfg *panel.ServerConfiguration) error {
	if cfg.Suspended {
		return m.addServerInternal(cfg, StateSuspended)
	}

	server := &Server{
		UUID:       cfg.UUID,
		InternalID: cfg.InternalID,
		Name:       cfg.Name,
		Config:     cfg,
		State:      StateOffline,
		docker:     m.docker,
		panel:      m.panel,
		dataDir:    m.dataDir,
	}

	m.mu.Lock()
	m.servers[cfg.UUID] = server
	m.mu.Unlock()

	log.Printf("Added server %s (%s)", cfg.Name, cfg.UUID)
	return nil
}

func (m *Manager) addServerInternal(cfg *panel.ServerConfiguration, state State) error {
	server := &Server{
		UUID:       cfg.UUID,
		InternalID: cfg.InternalID,
		Name:       cfg.Name,
		Config:     cfg,
		State:      state,
		docker:     m.docker,
		panel:      m.panel,
		dataDir:    m.dataDir,
	}

	m.mu.Lock()
	m.servers[cfg.UUID] = server
	m.mu.Unlock()

	return nil
}

func (m *Manager) RemoveServer(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, ok := m.servers[uuid]
	if !ok {
		return fmt.Errorf("server %s not found", uuid)
	}

	if srv.ContainerID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := m.docker.RemoveContainer(ctx, srv.ContainerID); err != nil {
			log.Printf("Warning: failed to remove container for %s: %v", uuid, err)
		}
	}

	delete(m.servers, uuid)
	return nil
}

func (m *Manager) GetServer(uuid string) (*Server, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.servers[uuid]
	return srv, ok
}

func (m *Manager) ListServers() []*Server {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Server, 0, len(m.servers))
	for _, srv := range m.servers {
		result = append(result, srv)
	}
	return result
}

func (s *Server) Start(ctx context.Context) error {
	log.Printf("[%s] Starting server...", s.UUID)

	s.mu.Lock()
	if s.State == StateRunning || s.State == StateStarting {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}
	s.State = StateStarting
	s.mu.Unlock()

	cfg := s.Config

	dockerCfg := environment.ContainerConfig{
		ServerUUID:  cfg.UUID,
		Image:       cfg.Container.Image,
		Entrypoint:  cfg.Container.Entrypoint,
		Invocation:  cfg.Invocation,
		MemoryLimit: cfg.MemoryLimit,
		SwapLimit:   cfg.SwapLimit,
		CPULimit:    cfg.CPULimit,
		DiskLimit:   cfg.DiskLimit,
		IOWeight:    cfg.IOWeight,
		OOMDisabled: cfg.OOMDisabled,
		Environment: cfg.Container.Environment,
		Mounts:      make([]environment.ContainerMount, len(cfg.Mounts)),
		Allocations: environment.ContainerAllocations{
			Default: environment.ContainerAllocation{
				ID:   cfg.Allocations.Default.ID,
				IP:   cfg.Allocations.Default.IP,
				Port: cfg.Allocations.Default.Port,
			},
			Extra: make([]environment.ContainerAllocation, 0, len(cfg.Allocations.Extra)),
		},
	}

	for i, m := range cfg.Mounts {
		dockerCfg.Mounts[i] = environment.ContainerMount{
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		}
	}

	for _, a := range cfg.Allocations.Extra {
		dockerCfg.Allocations.Extra = append(dockerCfg.Allocations.Extra, environment.ContainerAllocation{
			ID:   a.ID,
			IP:   a.IP,
			Port: a.Port,
		})
	}

	if err := s.docker.PullImage(ctx, cfg.Image); err != nil {
		s.setState(StateOffline)
		return fmt.Errorf("failed to pull image: %w", err)
	}

	containerID, err := s.docker.CreateServerContainer(ctx, dockerCfg)
	if err != nil {
		s.setState(StateOffline)
		return fmt.Errorf("failed to create container: %w", err)
	}

	s.ContainerID = containerID

	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		s.docker.RemoveContainer(ctx, containerID)
		s.ContainerID = ""
		s.setState(StateOffline)
		return fmt.Errorf("failed to start container: %w", err)
	}

	s.setState(StateRunning)
	log.Printf("[%s] Server started (container: %s)", s.UUID, containerID[:12])

	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	log.Printf("[%s] Stopping server...", s.UUID)

	s.mu.Lock()
	if s.State == StateOffline {
		s.mu.Unlock()
		return nil
	}
	s.State = StateStopping
	s.mu.Unlock()

	if s.ContainerID != "" {
		cfg := s.Config
		stopCmd := cfg.ProcessConfiguration.StopCommand

		if stopCmd != "" {
			if err := s.docker.SendStdin(ctx, s.ContainerID, stopCmd+"\n"); err != nil {
				log.Printf("[%s] Warning: failed to send stop command: %v", s.UUID, err)
			}
			time.Sleep(5 * time.Second)
		}

		if err := s.docker.StopContainer(ctx, s.ContainerID, nil); err != nil {
			log.Printf("[%s] Warning: force stopping container: %v", s.UUID, err)
			s.docker.KillContainer(ctx, s.ContainerID)
		}

		s.ContainerID = ""
	}

	s.setState(StateOffline)
	log.Printf("[%s] Server stopped", s.UUID)

	return nil
}

func (s *Server) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}

	time.Sleep(2 * time.Second)

	return s.Start(ctx)
}

func (s *Server) Kill(ctx context.Context) error {
	log.Printf("[%s] Killing server...", s.UUID)

	if s.ContainerID != "" {
		if err := s.docker.KillContainer(ctx, s.ContainerID); err != nil {
			return fmt.Errorf("failed to kill container: %w", err)
		}
		s.ContainerID = ""
	}

	s.setState(StateOffline)
	return nil
}

func (s *Server) SendCommand(ctx context.Context, command string) error {
	if s.ContainerID == "" {
		return fmt.Errorf("server not running")
	}
	return s.docker.SendStdin(ctx, s.ContainerID, command+"\n")
}

func (s *Server) GetLogs(ctx context.Context, tail string) (string, error) {
	if s.ContainerID == "" {
		return "", fmt.Errorf("server not running")
	}
	return s.docker.GetContainerLogs(ctx, s.ContainerID, tail)
}

func (s *Server) Attach(ctx context.Context) (io.ReadCloser, io.WriteCloser, error) {
	if s.ContainerID == "" {
		return nil, nil, fmt.Errorf("server not running")
	}
	return s.docker.AttachContainer(ctx, s.ContainerID)
}

func (s *Server) Install(ctx context.Context) error {
	log.Printf("[%s] Installing server...", s.UUID)

	install, err := s.panel.GetServerInstall(s.UUID)
	if err != nil {
		return fmt.Errorf("failed to get install script: %w", err)
	}

	s.mu.Lock()
	s.State = StateInstalling
	s.mu.Unlock()

	err = s.runInstallScript(ctx, install)
	successful := err == nil

	if reportErr := s.panel.ReportInstallStatus(s.UUID, successful, false); reportErr != nil {
		log.Printf("[%s] Warning: failed to report install status: %v", s.UUID, reportErr)
	}

	if err != nil {
		s.setState(StateOffline)
		return err
	}

	s.setState(StateOffline)
	log.Printf("[%s] Installation completed", s.UUID)
	return nil
}

func (s *Server) runInstallScript(ctx context.Context, install *panel.InstallResponse) error {
	script := install.Script
	if script == "" {
		return nil
	}

	scriptPath := filepath.Join(s.dataDir, "servers", s.UUID, "install.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return fmt.Errorf("failed to write install script: %w", err)
	}
	defer os.Remove(scriptPath)

	serverPath := filepath.Join(s.dataDir, "servers", s.UUID)

	dockerCfg := environment.ContainerConfig{
		ServerUUID:  s.UUID,
		Image:       install.ContainerImage,
		Mounts: []environment.ContainerMount{
			{Source: serverPath, Target: "/home/container"},
		},
		Environment: install.Config,
	}

	containerID, err := s.docker.CreateServerContainer(ctx, dockerCfg)
	if err != nil {
		return fmt.Errorf("failed to create install container: %w", err)
	}

	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to start install container: %w", err)
	}

	resultCh, err := s.docker.WaitForContainer(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to wait for install container: %w", err)
	}

	result := <-resultCh
	if result.Error != nil {
		return fmt.Errorf("install container error: %w", result.Error)
	}

	if result.StatusCode != 0 {
		logs, _ := s.docker.GetContainerLogs(ctx, containerID, "50")
		return fmt.Errorf("install script failed with status %d: %s", result.StatusCode, logs)
	}

	if err := s.docker.RemoveContainer(ctx, containerID); err != nil {
		log.Printf("[%s] Warning: failed to remove install container: %v", s.UUID, err)
	}

	return nil
}

func (s *Server) setState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
}

func (s *Server) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

func (s *Server) HandleCrash() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if now.Sub(s.lastCrash) > 5*time.Minute {
		s.crashCount = 0
	}

	s.crashCount++
	s.lastCrash = now

	s.State = StateOffline
	s.ContainerID = ""

	log.Printf("[%s] Server crashed (crash #%d)", s.UUID, s.crashCount)

	if s.Config.Settings.AutoRestartEnabled && s.Config.Settings.CrashDetectionEnabled {
		if s.crashCount <= 3 {
			log.Printf("[%s] Auto-restarting...", s.UUID)
			restartCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := s.Start(restartCtx); err != nil {
				log.Printf("[%s] Auto-restart failed: %v", s.UUID, err)
			}
		} else {
			log.Printf("[%s] Crash limit reached, not restarting", s.UUID)
		}
	}
}

func (m *Manager) crashLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			for _, srv := range m.servers {
				if srv.ContainerID != "" && srv.State == StateRunning {
					if !m.docker.IsContainerRunning(m.ctx, srv.ContainerID) {
						m.mu.RUnlock()
						srv.HandleCrash()
						m.mu.RLock()
					}
				}
			}
			m.mu.RUnlock()

			m.refreshServers()
		}
	}
}

func (m *Manager) refreshServers() {
	servers, err := m.panel.GetServers()
	if err != nil {
		return
	}

	panelUUIDs := make(map[string]bool)
	for _, srv := range servers.Data {
		panelUUIDs[srv.UUID] = true
	}

	m.mu.Lock()
	for _, srv := range servers.Data {
		if _, exists := m.servers[srv.UUID]; !exists {
			server := &Server{
				UUID:       srv.UUID,
				InternalID: srv.InternalID,
				Name:       srv.Name,
				Config:     &srv,
				State:      StateOffline,
				docker:     m.docker,
				panel:      m.panel,
				dataDir:    m.dataDir,
			}

			if srv.Suspended {
				server.State = StateSuspended
			}

			m.servers[srv.UUID] = server
			log.Printf("Discovered new server: %s (%s)", srv.Name, srv.UUID)
		}
	}

	for uuid := range m.servers {
		if !panelUUIDs[uuid] {
			log.Printf("Removing deleted server: %s", uuid)
			delete(m.servers, uuid)
		}
	}
	m.mu.Unlock()
}

func (s *Server) ServerPath() string {
	return filepath.Join(s.dataDir, "servers", s.UUID)
}

func ExtractServerUUID(containerName string) string {
	if !strings.HasPrefix(containerName, "royalwings_") {
		return ""
	}
	return strings.TrimPrefix(containerName, "royalwings_")
}
