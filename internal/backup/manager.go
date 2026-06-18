package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/royaldevlopments/royalwings/internal/panel"
	"github.com/royaldevlopments/royalwings/internal/server"
)

type Manager struct {
	directory string
	writeLimit int64
	manager   *server.Manager
	panel     *panel.Client
}

func NewManager(directory string, writeLimit int64, manager *server.Manager, panel *panel.Client) *Manager {
	return &Manager{
		directory: directory,
		writeLimit: writeLimit,
		manager:   manager,
		panel:     panel,
	}
}

func (m *Manager) CreateBackup(ctx context.Context, serverUUID, backupUUID string) error {
	srv, ok := m.manager.GetServer(serverUUID)
	if !ok {
		return fmt.Errorf("server %s not found", serverUUID)
	}

	serverPath := srv.ServerPath()
	backupDir := filepath.Join(m.directory, backupUUID)

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	backupFile := filepath.Join(backupDir, "backup.tar.gz")
	f, err := os.Create(backupFile)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(serverPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(serverPath, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if info.IsDir() {
			header.Name += "/"
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			written, err := io.Copy(tw, file)
			if err != nil {
				return err
			}

			if m.writeLimit > 0 && written > m.writeLimit {
				return fmt.Errorf("file %s exceeds write limit", relPath)
			}
		}

		return nil
	})

	if err != nil {
		os.RemoveAll(backupDir)
		return fmt.Errorf("backup failed: %w", err)
	}

	log.Printf("Backup %s created for server %s", backupUUID, serverUUID)
	return nil
}

func (m *Manager) RestoreBackup(ctx context.Context, serverUUID, backupUUID string) error {
	srv, ok := m.manager.GetServer(serverUUID)
	if !ok {
		return fmt.Errorf("server %s not found", serverUUID)
	}

	backupFile := filepath.Join(m.directory, backupUUID, "backup.tar.gz")
	f, err := os.Open(backupFile)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	serverPath := srv.ServerPath()

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		targetPath := filepath.Join(serverPath, header.Name)

		if !strings.HasPrefix(targetPath, serverPath) {
			return fmt.Errorf("illegal file path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}

			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			written, err := io.Copy(file, tr)
			file.Close()
			if err != nil {
				return err
			}

			if m.writeLimit > 0 && written > m.writeLimit {
				return fmt.Errorf("file %s exceeds write limit", header.Name)
			}
		}
	}

	log.Printf("Backup %s restored for server %s", backupUUID, serverUUID)
	return nil
}

func (m *Manager) DeleteBackup(backupUUID string) error {
	backupDir := filepath.Join(m.directory, backupUUID)
	return os.RemoveAll(backupDir)
}
