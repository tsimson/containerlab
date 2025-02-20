//go:build linux && podman
// +build linux,podman

package podman

import (
	"context"
	"fmt"
	"time"

	"github.com/containers/podman/v4/pkg/api/handlers"
	"github.com/containers/podman/v4/pkg/bindings/containers"
	"github.com/containers/podman/v4/pkg/bindings/images"
	"github.com/containers/podman/v4/pkg/bindings/network"
	dockerTypes "github.com/docker/docker/api/types"
	log "github.com/sirupsen/logrus"
	"github.com/srl-labs/containerlab/clab/exec"
	"github.com/srl-labs/containerlab/runtime"
	"github.com/srl-labs/containerlab/types"
	"github.com/srl-labs/containerlab/utils"
)

const (
	RuntimeName    = "podman"
	defaultTimeout = 120 * time.Second
)

type PodmanRuntime struct {
	config *runtime.RuntimeConfig
	mgmt   *types.MgmtNet
}

func init() {
	runtime.Register(RuntimeName, func() runtime.ContainerRuntime {
		return &PodmanRuntime{
			config: &runtime.RuntimeConfig{},
			mgmt:   &types.MgmtNet{},
		}
	})
}

// Init is used to initialize our runtime struct by calling all methods received from the caller
// Invokes methods such as WithConfig, WithMgmtNet etc to populate the fields.
func (r *PodmanRuntime) Init(opts ...runtime.RuntimeOption) error {
	for _, f := range opts {
		f(r)
	}
	return nil
}

func (r *PodmanRuntime) Mgmt() *types.MgmtNet { return r.mgmt }

func (r *PodmanRuntime) WithConfig(cfg *runtime.RuntimeConfig) {
	log.Debugf("Podman method WithConfig was called with cfg params: %+v", cfg)
	// Check for nil pointers on input
	if cfg == nil {
		log.Errorf("Method WithConfig has received a nil pointer")
		return
	}
	r.config = cfg
	if r.config.Timeout <= 0 {
		r.config.Timeout = defaultTimeout
	}
}

// WithMgmtNet assigns struct mgmt net parameters to the runtime struct.
func (r *PodmanRuntime) WithMgmtNet(net *types.MgmtNet) {
	// Check for nil pointers on input
	if net == nil {
		log.Errorf("Method WithMgmtNet has received a nil pointer")
		return
	}
	log.Debugf("Podman method WithMgmtNet was called with net params: %+v", net)
	r.mgmt = net
}

// WithKeepMgmtNet defines that we shouldn't delete mgmt network(s).
func (r *PodmanRuntime) WithKeepMgmtNet() {
	r.config.KeepMgmtNet = true
}

// CreateNet used to create a new bridge for clab mgmt network.
func (r *PodmanRuntime) CreateNet(ctx context.Context) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	log.Debugf("Trying to create a management network with params %+v", r.mgmt)
	// check the network existence first
	b, err := network.Exists(ctx, r.mgmt.Network, &network.ExistsOptions{})
	if err != nil {
		return err
	}
	// Create if the network doesn't exist
	if !b {
		netopts, err := r.netOpts(ctx)
		if err != nil {
			return err
		}
		log.Debugf("Trying to create mgmt network with params: %+v", netopts)
		resp, err := network.Create(ctx, &netopts)
		if err != nil {
			return err
		}
		log.Debugf("Create network response was: %+v", resp)
	}
	return err
}

// DeleteNet deletes a clab mgmt bridge.
func (r *PodmanRuntime) DeleteNet(ctx context.Context) error {
	// Skip if "keep mgmt" is set
	log.Debugf("Method DeleteNet was called with runtime inputs %+v and net settings %+v", r, r.mgmt)
	if r.config.KeepMgmtNet {
		return nil
	}
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	log.Debugf("trying to delete mgmt network %v", r.mgmt.Network)
	_, err = network.Remove(ctx, r.mgmt.Network, &network.RemoveOptions{})
	if err != nil {
		return fmt.Errorf("error while trying to remove a mgmt network %w", err)
	}
	return nil
}

func (r *PodmanRuntime) PullImageIfRequired(ctx context.Context, image string) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	// avoid short-hand image names
	// https://www.redhat.com/sysadmin/container-image-short-names
	canonicalImage := utils.GetCanonicalImageName(image)

	// Check the existence
	ex, err := images.Exists(ctx, canonicalImage, &images.ExistsOptions{})
	if err != nil {
		return err
	}
	// Pull the image if it doesn't exist
	if !ex {
		_, err = images.Pull(ctx, canonicalImage, &images.PullOptions{})
	}
	return err
}

// CreateContainer creates a container, but does not start it.
func (r *PodmanRuntime) CreateContainer(ctx context.Context, cfg *types.NodeConfig) (string, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return "", err
	}
	sg, err := r.createContainerSpec(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("error while trying to create a container spec for node %q: %w", cfg.LongName, err)
	}
	res, err := containers.CreateWithSpec(ctx, &sg, &containers.CreateOptions{})
	log.Debugf("Created a container with ID %v, warnings %v and error %v", res.ID, res.Warnings, err)
	return res.ID, err
}

// StartContainer starts a previously created container by ID or its name and executes post-start actions method.
func (r *PodmanRuntime) StartContainer(ctx context.Context, cID string, cfg *types.NodeConfig) (interface{}, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return nil, err
	}
	err = containers.Start(ctx, cID, &containers.StartOptions{})
	if err != nil {
		return nil, fmt.Errorf("error while starting a container %q: %w", cfg.LongName, err)
	}
	err = r.postStartActions(ctx, cID, cfg)
	if err != nil {
		return nil, fmt.Errorf("error while starting a container %q: %w", cfg.LongName, err)
	}
	return nil, nil
}

func (r *PodmanRuntime) PauseContainer(ctx context.Context, cID string) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	return containers.Pause(ctx, cID, &containers.PauseOptions{})
}

func (r *PodmanRuntime) UnpauseContainer(ctx context.Context, cID string) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	return containers.Unpause(ctx, cID, &containers.UnpauseOptions{})
}

func (r *PodmanRuntime) StopContainer(ctx context.Context, cID string) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	err = containers.Stop(ctx, cID, &containers.StopOptions{})
	if err != nil {
		return err
	}
	return nil
}

// ListContainers returns a list of all available containers in the system in a containerlab-specific struct.
func (r *PodmanRuntime) ListContainers(ctx context.Context, filters []*types.GenericFilter) ([]types.GenericContainer, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return nil, err
	}
	listOpts := new(containers.ListOptions).WithAll(true).WithFilters(r.buildFilterString(filters))
	cList, err := containers.List(ctx, listOpts)
	if err != nil {
		return nil, err
	}
	return r.produceGenericContainerList(ctx, cList)
}

func (r *PodmanRuntime) GetNSPath(ctx context.Context, cID string) (string, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return "", err
	}
	inspect, err := containers.Inspect(ctx, cID, &containers.InspectOptions{})
	if err != nil {
		return "", err
	}
	nspath := inspect.NetworkSettings.SandboxKey
	log.Debugf("Method GetNSPath was called with a resulting nspath %q", nspath)
	return nspath, nil
}

func (r *PodmanRuntime) Exec(ctx context.Context, cID string, execCmd *exec.ExecCmd) (exec.ExecResultHolder, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return nil, err
	}
	execCreateConf := handlers.ExecCreateConfig{
		ExecConfig: dockerTypes.ExecConfig{
			User:         "root",
			AttachStderr: true,
			AttachStdout: true,
			Cmd:          execCmd.GetCmd(),
		},
	}
	execID, err := containers.ExecCreate(ctx, cID, &execCreateConf)
	if err != nil {
		log.Errorf("failed to create exec in container %q: %v", cID, err)
		return nil, err
	}
	var sOut, sErr podmanWriterCloser
	execSAAOpts := new(containers.ExecStartAndAttachOptions).WithOutputStream(&sOut).WithErrorStream(
		&sErr).WithAttachOutput(true).WithAttachError(true)

	err = containers.ExecStartAndAttach(ctx, execID, execSAAOpts)
	if err != nil {
		log.Errorf("failed to start/attach exec in container %q: %v", cID, err)
		return nil, err
	}
	// perform inspection to retrieve the exitcode
	inspectOut, err := containers.ExecInspect(ctx, execID, nil)
	if err != nil {
		return nil, err
	}
	log.Debugf("Exec attached to the container %q and got stdout %q and stderr %q", cID, sOut.Bytes(), sErr.Bytes())

	// fill the execution result
	execResult := exec.NewExecResult(execCmd)
	execResult.SetStdErr(sErr.Bytes())
	execResult.SetStdOut(sOut.Bytes())
	execResult.SetReturnCode(inspectOut.ExitCode)

	return execResult, nil
}

func (r *PodmanRuntime) ExecNotWait(ctx context.Context, cID string, exec *exec.ExecCmd) error {
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	execCreateConf := handlers.ExecCreateConfig{
		ExecConfig: dockerTypes.ExecConfig{
			Tty:          false,
			AttachStderr: false,
			AttachStdout: false,
			Cmd:          exec.GetCmd(),
		},
	}
	execID, err := containers.ExecCreate(ctx, cID, &execCreateConf)
	if err != nil {
		log.Errorf("failed to create exec in container %q: %v", cID, err)
		return err
	}
	execSAAOpts := new(containers.ExecStartAndAttachOptions)
	err = containers.ExecStartAndAttach(ctx, execID, execSAAOpts)
	return err
}

// DeleteContainer removes a given container from the system (if it exists).
func (r *PodmanRuntime) DeleteContainer(ctx context.Context, contName string) error {
	force := !r.config.GracefulShutdown
	ctx, err := r.connect(ctx)
	if err != nil {
		return err
	}
	if !force {
		// Try to stop the containers first in case of graceful shutdown
		err = containers.Stop(ctx, contName, &containers.StopOptions{})
		if err != nil {
			log.Warnf("Unable to stop %q gracefully: %v", contName, err)
		}
	}
	// and do a force removal in the end
	force = true
	depend := true
	_, err = containers.Remove(ctx, contName, &containers.RemoveOptions{Force: &force, Depend: &depend})
	return err
}

// Config returns the runtime configuration options.
func (r *PodmanRuntime) Config() runtime.RuntimeConfig {
	return *r.config
}

// GetName returns runtime name as a string.
func (r *PodmanRuntime) GetName() string {
	return RuntimeName
}

// GetHostsPath returns fs path to a file which is mounted as /etc/hosts into a given container.
func (r *PodmanRuntime) GetHostsPath(ctx context.Context, cID string) (string, error) {
	ctx, err := r.connect(ctx)
	if err != nil {
		return "", err
	}
	inspect, err := containers.Inspect(ctx, cID, &containers.InspectOptions{})
	if err != nil {
		return "", err
	}
	hostsPath := inspect.HostsPath
	log.Debugf("Method GetHostsPath was called with a resulting path %q", hostsPath)
	return hostsPath, nil
}

// GetContainerStatus retrieves the ContainerStatus of the named container.
func (r *PodmanRuntime) GetContainerStatus(ctx context.Context, cID string) runtime.ContainerStatus {
	ctx, err := r.connect(ctx)
	if err != nil {
		return runtime.NotFound
	}
	icd, err := containers.Inspect(ctx, cID, nil)
	if err != nil {
		return runtime.NotFound
	}
	if icd.State.Running {
		return runtime.Running
	}
	return runtime.Stopped
}
