/*
Copyright 2017 Mirantis

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package integration

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/Mirantis/virtlet/pkg/api/virtlet.k8s/v1"
	"github.com/Mirantis/virtlet/pkg/config"
	"github.com/Mirantis/virtlet/pkg/manager"
	"github.com/Mirantis/virtlet/pkg/tapmanager"
)

const (
	virtletSocket    = "/tmp/virtlet.sock"
	disableKvmEnvVar = "VIRTLET_DISABLE_KVM"
)

type fakeFDManager struct{}

var fdManager tapmanager.FDManager

func (m *fakeFDManager) AddFDs(key string, data interface{}) ([]byte, error) {
	return nil, nil
}

func (m *fakeFDManager) ReleaseFDs(key string) error {
	return nil
}

func (m *fakeFDManager) Recover(key string, data interface{}) error {
	return nil
}

type fakeImageFileSystem struct {
	t     *testing.T
	inner http.FileSystem
}

func newFakeImageFileSystem(t *testing.T) http.FileSystem {
	return &fakeImageFileSystem{t: t, inner: http.Dir("/images")}
}

func (fs *fakeImageFileSystem) Open(name string) (http.File, error) {
	if name != "/cirros.img" && name != "/copy/cirros.img" {
		fs.t.Errorf("bad file name %q", name)
		return nil, fmt.Errorf("bad file name %q", name)
	}
	return fs.inner.Open("/cirros.img")
}

type VirtletManager struct {
	t       *testing.T
	ts      *httptest.Server
	tempDir string
	manager *manager.VirtletManager
	conn    *grpc.ClientConn
	doneCh  chan struct{}
}

func NewVirtletManager(t *testing.T) *VirtletManager {
	return &VirtletManager{t: t}
}

func (v *VirtletManager) startImageServer() {
	l, err := net.Listen("tcp", "127.0.0.1:80")
	if err != nil {
		v.t.Fatalf("can't listen on port 80: %v", err)
	}
	v.ts = httptest.NewUnstartedServer(http.FileServer(newFakeImageFileSystem(v.t)))
	v.ts.Listener = l
	v.ts.Start()
}

func (v *VirtletManager) Run() {
	if v.manager != nil {
		v.t.Fatalf("virtlet manager already started")
	}

	v.startImageServer()

	var err error
	v.tempDir, err = ioutil.TempDir("", "virtlet-manager")
	if err != nil {
		v.t.Fatalf("Can't create temp directory: %v", err)
	}

	os.Setenv("KUBERNETES_CLUSTER_URL", "")
	pstr := func(s string) *string { return &s }
	pbool := func(b bool) *bool { return &b }
	cfg := config.GetDefaultConfig()
	config.Override(cfg, &v1.VirtletConfig{
		DatabasePath:         pstr(filepath.Join(v.tempDir, "virtlet.db")),
		DownloadProtocol:     pstr("http"),
		ImageDir:             pstr(filepath.Join(v.tempDir, "images")),
		SkipImageTranslation: pbool(true),
		LibvirtURI:           pstr(libvirtURI),
		RawDevices:           pstr("loop*"),
		CRISocketPath:        pstr(virtletSocket),
		DisableLogging:       pbool(true),
		DisableKVM:           pbool(os.Getenv(disableKvmEnvVar) != ""),
	})
	v.manager = manager.NewVirtletManager(cfg, &fakeFDManager{}, nil)
	v.doneCh = make(chan struct{})
	go func() {
		if err := v.manager.Run(); err != nil {
			v.t.Logf("VirtletManager result (expect closed network connection error): %v", err)
		}
		v.manager = nil
		close(v.doneCh)
	}()

	if err := waitForSocket(virtletSocket); err != nil {
		v.t.Fatalf("Couldn't connect to virtlet socket: %v", err)
	}

	v.conn, err = grpc.Dial(virtletSocket, grpc.WithInsecure(), grpc.WithDialer(Dial))
	if err != nil {
		v.t.Fatalf("Couldn't connect to virtlet socket: %v", err)
	}
}

func (v *VirtletManager) Close() {
	if v.manager == nil {
		v.t.Fatalf("virtlet manager not started")
	}
	v.manager.Stop()
	os.RemoveAll(v.tempDir)
	v.ts.Close()
	<-v.doneCh
}

func Dial(socket string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", socket, timeout)
}
