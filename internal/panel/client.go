package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/royaldevlopments/royalwings/internal/config"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(cfg config.PanelConfig) *Client {
	return &Client{
		baseURL: cfg.URL,
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) request(method, path string, body, result interface{}) error {
	url := fmt.Sprintf("%s/api/remote%s", c.baseURL, path)

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("panel returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

func (c *Client) Get(path string, result interface{}) error {
	return c.request(http.MethodGet, path, nil, result)
}

func (c *Client) Post(path string, body, result interface{}) error {
	return c.request(http.MethodPost, path, body, result)
}

func (c *Client) GetServers() (*ServerListResponse, error) {
	var result ServerListResponse
	if err := c.Get("/servers", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetServer(uuid string) (*ServerConfiguration, error) {
	var result ServerConfiguration
	if err := c.Get(fmt.Sprintf("/servers/%s", uuid), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetServerInstall(uuid string) (*InstallResponse, error) {
	var result InstallResponse
	if err := c.Get(fmt.Sprintf("/servers/%s/install", uuid), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ReportInstallStatus(uuid string, successful bool, reinstall bool) error {
	body := map[string]interface{}{
		"successful": successful,
	}
	return c.Post(fmt.Sprintf("/servers/%s/install", uuid), body, nil)
}

func (c *Client) ReportTransferFailure(uuid string) error {
	return c.Post(fmt.Sprintf("/servers/%s/transfer/failure", uuid), nil, nil)
}

func (c *Client) ReportTransferSuccess(uuid string) error {
	return c.Post(fmt.Sprintf("/servers/%s/transfer/success", uuid), nil, nil)
}

func (c *Client) SubmitActivity(events []ActivityEvent) error {
	return c.Post("/activity", map[string]interface{}{
		"events": events,
	}, nil)
}

func (c *Client) AuthenticateSFTP(username, password string) (*SFTPAuthResponse, error) {
	var result SFTPAuthResponse
	if err := c.Post("/sftp/auth", map[string]string{
		"username": username,
		"password": password,
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ResetServerStates() error {
	return c.Post("/servers/reset", nil, nil)
}

type ServerListResponse struct {
	Data []ServerConfiguration `json:"data"`
}

type ServerConfiguration struct {
	Settings       ServerSettings       `json:"settings"`
	ProcessConfiguration ProcessConfiguration `json:"process_configuration"`
	UUID           string               `json:"uuid"`
	InternalID     int                  `json:"internal_id"`
	Node           string               `json:"node"`
	Name           string               `json:"name"`
	Description    string               `json:"description"`
	Suspended      bool                 `json:"suspended"`
	Environment    ServerEnvironment    `json:"environment"`
	Invocation     string               `json:"invocation"`
	SkipScripts    bool                 `json:"skip_scripts"`
	Image          string               `json:"image"`
	EggID          int                  `json:"egg_id"`
	MemoryLimit    int64                `json:"memory_limit"`
	SwapLimit      int64                `json:"swap_limit"`
	CPULimit       int64                `json:"cpu_limit"`
	DiskLimit      int64                `json:"disk_limit"`
	IOWeight       int                  `json:"io_weight"`
	OOMDisabled    bool                 `json:"oom_disabled"`
	Allocations    Allocations          `json:"allocations"`
	Mounts         []Mount              `json:"mounts"`
	Egg            Egg                  `json:"egg"`
	Container      ContainerConfig      `json:"container"`
}

type ServerSettings struct {
	CrashDetectionEnabled bool `json:"crash_detection_enabled"`
	AutoRestartEnabled    bool `json:"auto_restart_enabled"`
}

type ProcessConfiguration struct {
	StartupCommand string `json:"startup_command"`
	StopCommand    string `json:"stop_command"`
}

type ServerEnvironment struct {
	Variables []EnvironmentVariable `json:"variables"`
}

type EnvironmentVariable struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Default     string `json:"default_value"`
	UserEditable bool   `json:"user_editable"`
	Rules       string `json:"rules"`
}

type Allocations struct {
	Default Allocation `json:"default"`
	Mappings []AllocationMapping `json:"mappings"`
	Extra    []Allocation `json:"extra"`
}

type Allocation struct {
	ID       int    `json:"id"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Alias    string `json:"alias"`
	Notes    string `json:"notes"`
}

type AllocationMapping map[string]int

type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	ReadOnly bool `json:"read_only"`
}

type Egg struct {
	ID               int              `json:"id"`
	UUID             string           `json:"uuid"`
	Name             string           `json:"name"`
	DockerImages     map[string]string `json:"docker_images"`
}

type ContainerConfig struct {
	Image     string   `json:"image"`
	Entrypoint string  `json:"entrypoint"`
	Environment map[string]string `json:"environment"`
}

type InstallResponse struct {
	ContainerImage string            `json:"container_image"`
	Entrypoint     string            `json:"entrypoint"`
	Script         string            `json:"script"`
	Config         map[string]string `json:"config"`
}

type SFTPAuthResponse struct {
	ServerUUID   string        `json:"server_uuid"`
	UserUUID     string        `json:"user_uuid"`
	Permissions  []string      `json:"permissions"`
}

type ActivityEvent struct {
	Event      string `json:"event"`
	ServerUUID string `json:"server_uuid,omitempty"`
	UserUUID   string `json:"user_uuid,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Timestamp  string `json:"timestamp"`
	IP         string `json:"ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
}
