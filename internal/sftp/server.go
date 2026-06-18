package sftp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"github.com/royaldevlopments/royalwings/internal/panel"
	"github.com/royaldevlopments/royalwings/internal/server"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	address  string
	port     int
	manager  *server.Manager
	panel    *panel.Client
	dataDir  string
	hostKey  ssh.Signer
}

func New(address string, port int, manager *server.Manager, panel *panel.Client, dataDir string) (*Server, error) {
	return &Server{
		address: address,
		port:    port,
		manager: manager,
		panel:   panel,
		dataDir: dataDir,
	}, nil
}

func (s *Server) Start() error {
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return s.authenticate(conn.User(), string(password))
		},
		AuthLogCallback: func(conn ssh.ConnMetadata, method string, err error) {
			if err != nil {
				log.Printf("SFTP auth failed for %s: %v", conn.User(), err)
			}
		},
	}

	key, err := s.getHostKey()
	if err != nil {
		return fmt.Errorf("failed to get host key: %w", err)
	}
	config.AddHostKey(key)

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.address, s.port))
	if err != nil {
		return fmt.Errorf("failed to listen on %s:%d: %w", s.address, s.port, err)
	}

	log.Printf("SFTP server listening on %s:%d", s.address, s.port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("SFTP accept error: %v", err)
			continue
		}

		go s.handleConnection(conn, config)
	}
}

func (s *Server) handleConnection(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		log.Printf("SSX handshake failed: %v", err)
		return
	}
	defer sshConn.Close()

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("Failed to accept channel: %v", err)
			continue
		}

		go func(in <-chan *ssh.Request) {
			for req := range in {
				switch req.Type {
				case "subsystem":
					if string(req.Payload[4:]) == "sftp" {
						req.Reply(true, nil)
						go s.serveSFTP(channel, sshConn)
					} else {
						req.Reply(false, nil)
					}
				default:
					req.Reply(false, nil)
				}
			}
		}(requests)
	}
}

func (s *Server) serveSFTP(channel ssh.Channel, sshConn *ssh.ServerConn) {
	serverRoot := filepath.Join(s.dataDir, "servers", sshConn.Permissions.Extensions["server_uuid"])

	server := sftp.NewRequestServer(channel, sftp.Handlers{
		FileGet:  &sftpFileReader{root: serverRoot},
		FilePut:  &sftpFileWriter{root: serverRoot},
		FileCmd:  &sftpFileCommander{root: serverRoot},
		FileList: &sftpFileLister{root: serverRoot},
	})

	if err := server.Serve(); err != nil {
		if err != io.EOF {
			log.Printf("SFTP serve error: %v", err)
		}
	}
}

func (s *Server) authenticate(username, password string) (*ssh.Permissions, error) {
	parts := parseUsername(username)
	if parts == nil {
		return nil, fmt.Errorf("invalid username format")
	}

	authResp, err := s.panel.AuthenticateSFTP(username, password)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	srv, ok := s.manager.GetServer(authResp.ServerUUID)
	if !ok {
		return nil, fmt.Errorf("server not found")
	}

	serverPath := srv.ServerPath()
	if err := os.MkdirAll(serverPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create server directory")
	}

	perms := &ssh.Permissions{
		Extensions: map[string]string{
			"server_uuid": authResp.ServerUUID,
			"user_uuid":   authResp.UserUUID,
			"server_path": serverPath,
		},
	}

	return perms, nil
}

func (s *Server) getHostKey() (ssh.Signer, error) {
	keyPath := filepath.Join(s.dataDir, ".ssh", "royalwings_rsa")
	os.MkdirAll(filepath.Dir(keyPath), 0700)

	keyData, err := os.ReadFile(keyPath)
	if err == nil {
		return ssh.ParsePrivateKey(keyData)
	}

	log.Printf("Generating new SSH host key at %s", keyPath)
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("failed to generate host key: %w", err)
	}

	pemData := x509.MarshalPKCS1PrivateKey(key)
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: pemData,
	}

	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		return nil, fmt.Errorf("failed to write host key: %w", err)
	}

	return ssh.NewSignerFromKey(key)
}

func parseUsername(username string) []string {
	parts := strings.SplitN(username, ".", 2)
	if len(parts) != 2 {
		return nil
	}
	return parts
}

type sftpFileReader struct {
	root string
}

func (r *sftpFileReader) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	path := filepath.Join(r.root, request.Filepath)
	if !strings.HasPrefix(path, r.root) {
		return nil, os.ErrPermission
	}
	return os.Open(path)
}

type sftpFileWriter struct {
	root string
}

func (w *sftpFileWriter) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	path := filepath.Join(w.root, request.Filepath)
	if !strings.HasPrefix(path, w.root) {
		return nil, os.ErrPermission
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
}

type sftpFileCommander struct {
	root string
}

func (c *sftpFileCommander) Filecmd(request *sftp.Request) error {
	path := filepath.Join(c.root, request.Filepath)
	target := request.Target
	if target != "" {
		target = filepath.Join(c.root, target)
	}

	if !strings.HasPrefix(path, c.root) {
		return os.ErrPermission
	}
	if target != "" && !strings.HasPrefix(target, c.root) {
		return os.ErrPermission
	}

	switch request.Method {
	case "Mkdir":
		return os.MkdirAll(path, 0755)
	case "Rmdir":
		return os.RemoveAll(path)
	case "Remove":
		return os.Remove(path)
	case "Rename":
		return os.Rename(path, target)
	case "Symlink":
		return os.Symlink(request.Filepath, target)
	}

	return nil
}

type sftpFileLister struct {
	root string
}

func (l *sftpFileLister) Filelist(request *sftp.Request) (sftp.ListerAt, error) {
	path := filepath.Join(l.root, request.Filepath)
	if !strings.HasPrefix(path, l.root) {
		return nil, os.ErrPermission
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	fileInfos := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fileInfos = append(fileInfos, info)
	}

	return &listerAtWrapper{files: fileInfos}, nil
}

type listerAtWrapper struct {
	files []os.FileInfo
	index int
}

func (l *listerAtWrapper) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.files)) {
		return 0, io.EOF
	}

	n := copy(ls, l.files[offset:])
	if n < len(l.files)-int(offset) {
		return n, nil
	}

	return n, io.EOF
}
