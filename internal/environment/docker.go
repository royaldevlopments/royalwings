package environment

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

type Docker struct {
	cli          *client.Client
	network      string
	dataDir      string
}

func NewDocker(socket, network, dataDir string) (*Docker, error) {
	opts := []client.Opt{
		client.WithVersion("1.44"),
	}

	if socket != "" {
		opts = append(opts, client.WithHost(socket))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Docker{
		cli:     cli,
		network: network,
		dataDir: dataDir,
	}, nil
}

func (d *Docker) Close() error {
	return d.cli.Close()
}

func (d *Docker) EnsureNetwork(ctx context.Context) error {
	networks, err := d.cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	for _, n := range networks {
		if n.Name == d.network {
			return nil
		}
	}

	_, err = d.cli.NetworkCreate(ctx, d.network, types.NetworkCreate{
		Driver: "bridge",
	})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	return nil
}

func (d *Docker) PullImage(ctx context.Context, imageName string) error {
	reader, err := d.cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	io.Copy(io.Discard, reader)
	return nil
}

func (d *Docker) CreateServerContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	serverPath := filepath.Join(d.dataDir, "servers", cfg.ServerUUID)

	if err := os.MkdirAll(serverPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create server directory: %w", err)
	}

	envVars := make([]string, 0, len(cfg.Environment)+4)
	for k, v := range cfg.Environment {
		envVars = append(envVars, fmt.Sprintf("%s=%s", k, v))
	}
	envVars = append(envVars,
		fmt.Sprintf("SERVER_UUID=%s", cfg.ServerUUID),
		fmt.Sprintf("SERVER_IP=%s", cfg.Allocations.Default.IP),
		fmt.Sprintf("SERVER_PORT=%d", cfg.Allocations.Default.Port),
	)

	portSet := nat.PortSet{}
	portMap := nat.PortMap{}

	for _, alloc := range cfg.Allocations.Extra {
		port := nat.Port(fmt.Sprintf("%d/tcp", alloc.Port))
		portSet[port] = struct{}{}
		portMap[port] = []nat.PortBinding{
			{HostIP: alloc.IP, HostPort: fmt.Sprintf("%d", alloc.Port)},
		}
	}

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: serverPath,
			Target: "/home/container",
		},
	}

	for _, m := range cfg.Mounts {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	resources := container.Resources{
		Memory:     cfg.MemoryLimit * 1024 * 1024,
		MemorySwap: (cfg.MemoryLimit + cfg.SwapLimit) * 1024 * 1024,
		CPUQuota:   cfg.CPULimit * 1000,
		CPUPeriod:  100000,
	}

	if cfg.OOMDisabled {
		resources.OomKillDisable = &cfg.OOMDisabled
	}



	containerName := fmt.Sprintf("royalwings_%s", cfg.ServerUUID[:12])

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:      cfg.Image,
		Cmd:        []string{cfg.Invocation},
		Env:        envVars,
		ExposedPorts: portSet,
		Labels: map[string]string{
			"royalwings:server_uuid": cfg.ServerUUID,
			"royalwings:managed":     "true",
		},
		Tty:        true,
		OpenStdin:  true,
		WorkingDir: "/home/container",
	}, &container.HostConfig{
		PortBindings: portMap,
		Mounts:       mounts,
		Resources:    resources,
		NetworkMode:  container.NetworkMode(d.network),
	}, nil, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

func (d *Docker) StartContainer(ctx context.Context, containerID string) error {
	return d.cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
}

func (d *Docker) StopContainer(ctx context.Context, containerID string, timeout *int) error {
	var timeoutDuration *time.Duration
	if timeout != nil {
		d := time.Duration(*timeout) * time.Second
		timeoutDuration = &d
	}

	return d.cli.ContainerStop(ctx, containerID, timeoutDuration)
}

func (d *Docker) RestartContainer(ctx context.Context, containerID string, timeout *int) error {
	var timeoutDuration *time.Duration
	if timeout != nil {
		d := time.Duration(*timeout) * time.Second
		timeoutDuration = &d
	}

	return d.cli.ContainerRestart(ctx, containerID, timeoutDuration)
}

func (d *Docker) RemoveContainer(ctx context.Context, containerID string) error {
	return d.cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{
		Force: true,
	})
}

func (d *Docker) KillContainer(ctx context.Context, containerID string) error {
	return d.cli.ContainerKill(ctx, containerID, "SIGKILL")
}

func (d *Docker) SendStdin(ctx context.Context, containerID string, input string) error {
	resp, err := d.cli.ContainerAttach(ctx, containerID, types.ContainerAttachOptions{
		Stdin:  true,
		Stream: true,
	})
	if err != nil {
		return fmt.Errorf("failed to attach to container: %w", err)
	}
	defer resp.Close()

	_, err = resp.Conn.Write([]byte(input))
	return err
}

func (d *Docker) AttachContainer(ctx context.Context, containerID string) (io.ReadCloser, io.WriteCloser, error) {
	resp, err := d.cli.ContainerAttach(ctx, containerID, types.ContainerAttachOptions{
		Stdin:  true,
		Stdout: true,
		Stderr: true,
		Stream: true,
		Logs:   true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to attach to container: %w", err)
	}

	return &attachReadCloser{Reader: resp.Reader, closer: func() error { resp.Close(); return nil }}, resp.Conn, nil
}

type attachReadCloser struct {
	io.Reader
	closer func() error
}

func (a *attachReadCloser) Close() error {
	return a.closer()
}

func (d *Docker) GetContainerLogs(ctx context.Context, containerID string, tail string) (string, error) {
	options := types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
		Timestamps: false,
	}

	reader, err := d.cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", fmt.Errorf("failed to get container logs: %w", err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	stdcopy.StdCopy(&buf, &buf, reader)
	return buf.String(), nil
}

func (d *Docker) ContainerExists(ctx context.Context, containerID string) bool {
	_, err := d.cli.ContainerInspect(ctx, containerID)
	return err == nil
}

func (d *Docker) IsContainerRunning(ctx context.Context, containerID string) bool {
	insp, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}
	return insp.State.Running
}

func (d *Docker) WaitForContainer(ctx context.Context, containerID string) (<-chan ContainerWaitResult, error) {
	statusCh, errCh := d.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	resultCh := make(chan ContainerWaitResult, 1)

	go func() {
		select {
		case status := <-statusCh:
			var waitErr error
			if status.Error != nil {
				waitErr = fmt.Errorf(status.Error.Message)
			}
			resultCh <- ContainerWaitResult{
				StatusCode: status.StatusCode,
				Error:      waitErr,
			}
		case err := <-errCh:
			resultCh <- ContainerWaitResult{
				StatusCode: -1,
				Error:      err,
			}
		case <-ctx.Done():
			resultCh <- ContainerWaitResult{
				StatusCode: -1,
				Error:      ctx.Err(),
			}
		}
	}()

	return resultCh, nil
}

type ContainerConfig struct {
	ServerUUID   string
	Image        string
	Entrypoint   string
	Invocation   string
	MemoryLimit  int64
	SwapLimit    int64
	CPULimit     int64
	DiskLimit    int64
	IOWeight     int
	OOMDisabled  bool
	Environment  map[string]string
	Mounts       []ContainerMount
	Allocations  ContainerAllocations
}

type ContainerMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type ContainerAllocations struct {
	Default   ContainerAllocation
	Mappings  []ContainerAllocation
	Extra     []ContainerAllocation
}

type ContainerAllocation struct {
	ID   int
	IP   string
	Port int
}

type ContainerWaitResult struct {
	StatusCode int64
	Error      error
}
