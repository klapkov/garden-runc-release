package nerd_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/guardian/metrics"
	"code.cloudfoundry.org/guardian/rundmc/runcontainerd"
	"code.cloudfoundry.org/guardian/rundmc/runcontainerd/nerd"
	"code.cloudfoundry.org/lager/v3/lagertest"
	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type deadlineContainersServer struct {
	containersapi.UnimplementedContainersServer
	calls    atomic.Int32
	failures int32
	code     codes.Code
}

func (s *deadlineContainersServer) Get(context.Context, *containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
	if s.calls.Add(1) <= s.failures {
		return nil, status.Error(s.code, "container lookup failure")
	}
	return &containersapi.GetContainerResponse{Container: &containersapi.Container{ID: "retry-test"}}, nil
}

type deadlineTasksServer struct {
	tasksapi.UnimplementedTasksServer
	calls      atomic.Int32
	failStatus bool
}

func (s *deadlineTasksServer) Get(context.Context, *tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
	// First Get loads the task. Second Get reads its status.
	if s.calls.Add(1) == 2 && s.failStatus {
		return nil, status.Error(codes.DeadlineExceeded, "transient status failure")
	}
	return &tasksapi.GetResponse{Process: &tasktypes.Process{ContainerID: "retry-test", ID: "retry-test", Pid: 12345, Status: tasktypes.Status_RUNNING}}, nil
}

func TestNerdDeadlineRetries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		loadFailures  int32
		loadCode      codes.Code
		statusFailure bool
		timeout       time.Duration
		wantLoadCalls int32
		wantTaskCalls int32
		wantError     bool
	}{
		{"load recovers", 1, codes.DeadlineExceeded, false, 5 * time.Second, 2, 2, false},
		{"status recovers", 0, codes.OK, true, 5 * time.Second, 1, 3, false},
		{"not found is not retried", 1, codes.NotFound, false, 5 * time.Second, 1, 0, true},
		{"permission denied is not retried", 1, codes.PermissionDenied, false, 5 * time.Second, 1, 0, true},
		{"retry budget exhausted", 10, codes.DeadlineExceeded, false, 5 * time.Second, 5, 0, true},
		{"cancel load retry", 10, codes.DeadlineExceeded, false, 200 * time.Millisecond, 1, 0, true},
		{"cancel status retry", 0, codes.OK, true, 200 * time.Millisecond, 1, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			listener, err := net.Listen("unix", filepath.Join(dir, "containerd.sock"))
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			containerRPC := &deadlineContainersServer{failures: tc.loadFailures, code: tc.loadCode}
			taskRPC := &deadlineTasksServer{failStatus: tc.statusFailure}
			containersapi.RegisterContainersServer(server, containerRPC)
			tasksapi.RegisterTasksServer(server, taskRPC)
			go server.Serve(listener)
			defer server.Stop()
			c, err := client.New(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), "retry-test"), tc.timeout)
			defer cancel()
			log := lagertest.NewTestLogger("retry-test")
			n := nerd.New(c, ctx, dir, metrics.NewMetricsProvider(log, dir), runcontainerd.RunscRuntime)
			pid, state, err := n.State(log, "retry-test")
			if (err != nil) != tc.wantError {
				t.Fatalf("State: pid=%d state=%s err=%v", pid, state, err)
			}
			if !tc.wantError && (pid != 12345 || state != "running") {
				t.Fatalf("wrong process state: %d %s", pid, state)
			}
			if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
				t.Fatalf("lost context error: %v", err)
			}
			if got := containerRPC.calls.Load(); got != tc.wantLoadCalls {
				t.Errorf("load calls=%d want=%d", got, tc.wantLoadCalls)
			}
			if got := taskRPC.calls.Load(); got != tc.wantTaskCalls {
				t.Errorf("task calls=%d want=%d", got, tc.wantTaskCalls)
			}
		})
	}
}
