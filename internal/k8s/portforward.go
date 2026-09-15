package k8s

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForward is a running local-to-pod forward.
type PortForward struct {
	Namespace string
	Pod       string
	Local     uint16
	Remote    uint16

	stop  chan struct{}
	done  chan error
	ready chan struct{}
}

// ContainerPorts returns the distinct containerPort numbers a pod exposes,
// in declaration order.
func ContainerPorts(pod *unstructured.Unstructured) []uint16 {
	var ports []uint16
	seen := map[uint16]bool{}
	containers, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
	for _, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ps, _, _ := unstructured.NestedSlice(cm, "ports")
		for _, p := range ps {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := pm["containerPort"].(int64); ok && !seen[uint16(n)] {
				seen[uint16(n)] = true
				ports = append(ports, uint16(n))
			}
		}
	}
	return ports
}

// ForwardPort starts forwarding a local port (0 picks a free one) to a
// pod's remote port. It returns once the tunnel is ready or fails to start.
// Call Stop to tear it down.
func (c *Client) ForwardPort(namespace, pod string, local, remote uint16) (*PortForward, error) {
	rt, upgrader, err := spdy.RoundTripperFor(c.cfg)
	if err != nil {
		return nil, fmt.Errorf("port-forward transport: %w", err)
	}
	req := c.rest.Post().AbsPath(append(resourcePath(Pods.GVR, namespace), pod, "portforward")...)
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, "POST", req.URL())

	pf := &PortForward{
		Namespace: namespace, Pod: pod, Remote: remote,
		stop: make(chan struct{}), done: make(chan error, 1), ready: make(chan struct{}),
	}
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("%d:%d", local, remote)}, pf.stop, pf.ready, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("port-forward: %w", err)
	}
	go func() { pf.done <- fw.ForwardPorts() }()

	select {
	case <-pf.ready:
		ports, err := fw.GetPorts()
		if err != nil || len(ports) == 0 {
			pf.Stop()
			return nil, fmt.Errorf("port-forward: could not resolve local port: %w", err)
		}
		pf.Local = ports[0].Local
		return pf, nil
	case err := <-pf.done:
		if err == nil {
			err = fmt.Errorf("port-forward closed before it was ready")
		}
		return nil, fmt.Errorf("port-forward %s/%s %d: %w", namespace, pod, remote, err)
	case <-time.After(10 * time.Second):
		pf.Stop()
		return nil, fmt.Errorf("port-forward %s/%s %d: timed out establishing tunnel", namespace, pod, remote)
	}
}

// Wait reports when the forward stops, returning its error if any. It is
// safe to call once; use with a goroutine that emits a UI message.
func (pf *PortForward) Wait(ctx context.Context) error {
	select {
	case err := <-pf.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop tears down the forward. It is idempotent.
func (pf *PortForward) Stop() {
	select {
	case <-pf.stop:
	default:
		close(pf.stop)
	}
}

// Addr is the local address the forward listens on.
func (pf *PortForward) Addr() string { return "127.0.0.1:" + strconv.Itoa(int(pf.Local)) }

// NewTestForward builds a PortForward without a live tunnel, for tests.
func NewTestForward(namespace, pod string, local, remote uint16) *PortForward {
	pf := &PortForward{
		Namespace: namespace, Pod: pod, Local: local, Remote: remote,
		stop: make(chan struct{}), done: make(chan error, 1), ready: make(chan struct{}),
	}
	return pf
}
