// Command gpu-device-plugin advertises this host's DRM render nodes as a
// schedulable Kubernetes resource (default `amd.com/render`).
//
// It implements the kubelet Device Plugin API:
//   - ListAndWatch advertises the render devices found under the render path;
//   - Allocate tells the container runtime to mount the granted render node
//     into the requesting Pod.
//
// That buys scheduling and automatic device mounting. It does NOT enforce
// per-Pod VRAM limits -- the hardware exposes none (see
// research/native-k3s-gpu-models-plan.md section 7).
package main

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	resourceName = env("RESOURCE_NAME", "amd.com/render")
	renderPath   = env("RENDER_PATH", "/dev/dri")
	deviceDir    = env("DEVICE_PLUGIN_DIR", "/var/lib/kubelet/device-plugins")
	pluginSocket = filepath.Join(deviceDir, "amd-render.sock")
	kubeletSock  = filepath.Join(deviceDir, "kubelet.sock")
)

type devicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer
	devices []*pluginapi.Device
}

// discoverRenderNodes lists the DRM render nodes (renderD*) under renderPath.
func discoverRenderNodes() ([]*pluginapi.Device, error) {
	matches, err := filepath.Glob(filepath.Join(renderPath, "renderD*"))
	if err != nil {
		return nil, err
	}
	devices := make([]*pluginapi.Device, 0, len(matches))
	for _, m := range matches {
		devices = append(devices, &pluginapi.Device{ID: filepath.Base(m), Health: pluginapi.Healthy})
	}
	return devices, nil
}

func (p *devicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{}, nil
}

func (p *devicePlugin) ListAndWatch(_ *pluginapi.Empty, srv pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
		return err
	}
	// The device set is static on a single node; re-send so a reconnecting
	// kubelet always converges.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := srv.Send(&pluginapi.ListAndWatchResponse{Devices: p.devices}); err != nil {
			return err
		}
	}
	return nil
}

func (p *devicePlugin) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := &pluginapi.AllocateResponse{}
	for _, r := range req.GetContainerRequests() {
		specs := make([]*pluginapi.DeviceSpec, 0, len(r.GetDevicesIDs()))
		for _, id := range r.GetDevicesIDs() {
			host := filepath.Join(renderPath, id)
			specs = append(specs, &pluginapi.DeviceSpec{
				ContainerPath: host,
				HostPath:      host,
				Permissions:   "rw",
			})
		}
		resp.ContainerResponses = append(resp.ContainerResponses, &pluginapi.ContainerAllocateResponse{
			Devices: specs,
			Mounts: []*pluginapi.Mount{{
				ContainerPath: renderPath,
				HostPath:      renderPath,
				ReadOnly:      false,
			}},
		})
	}
	return resp, nil
}

func register() error {
	conn, err := grpc.NewClient("unix://"+kubeletSock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = pluginapi.NewRegistrationClient(conn).Register(context.Background(), &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     filepath.Base(pluginSocket),
		ResourceName: resourceName,
	})
	return err
}

func main() {
	devices, err := discoverRenderNodes()
	if err != nil {
		log.Fatalf("discover render nodes under %s: %v", renderPath, err)
	}
	if len(devices) == 0 {
		log.Fatalf("no render nodes found under %s", renderPath)
	}
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, d.ID)
	}
	log.Printf("advertising %d render node(s) %v as %s", len(devices), names, resourceName)

	_ = os.Remove(pluginSocket)
	listener, err := net.Listen("unix", pluginSocket)
	if err != nil {
		log.Fatalf("listen on %s: %v", pluginSocket, err)
	}
	server := grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(server, &devicePlugin{devices: devices})

	go func() {
		if err := server.Serve(listener); err != nil {
			log.Fatalf("serve %s: %v", pluginSocket, err)
		}
	}()

	time.Sleep(time.Second) // let the socket come up before registering
	if err := register(); err != nil {
		log.Fatalf("register %s with kubelet: %v", resourceName, err)
	}
	log.Printf("registered %s with kubelet", resourceName)
	select {}
}
