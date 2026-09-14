package k8s

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// LogOptions controls a log stream.
type LogOptions struct {
	Follow    bool
	TailLines int64         // 0 means server default (all)
	Since     time.Duration // 0 means no limit
	Previous  bool          // logs of the previous container instance
}

// LogLine is one line from a container, with the server timestamp parsed.
type LogLine struct {
	Container string
	Time      time.Time
	Text      string
}

// LogEvent is a line or the end of a stream.
type LogEvent struct {
	Line LogLine
	// Err is set when the stream ended: nil for a clean end, otherwise the
	// failure. Line is empty in that case.
	Ended bool
	Err   error
}

// Logs streams a container's log. Timestamps are always requested from
// the server so the UI can show or hide them and merge containers in
// order. The channel closes after an Ended event or when ctx ends.
func (c *Client) Logs(ctx context.Context, namespace, pod, container string, opts LogOptions) (<-chan LogEvent, error) {
	segs := append(resourcePath(Pods.GVR, namespace), pod, "log")
	req := c.rest.Get().AbsPath(segs...).
		Param("container", container).
		Param("timestamps", "true")
	if opts.Follow {
		req = req.Param("follow", "true")
	}
	if opts.TailLines > 0 {
		req = req.Param("tailLines", strconv.FormatInt(opts.TailLines, 10))
	}
	if opts.Since > 0 {
		req = req.Param("sinceSeconds", strconv.FormatInt(int64(opts.Since.Seconds()), 10))
	}
	if opts.Previous {
		req = req.Param("previous", "true")
	}
	body, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("logs %s/%s[%s]: %w", namespace, pod, container, err)
	}

	out := make(chan LogEvent, 256)
	go func() {
		defer close(out)
		defer func() { _ = body.Close() }()
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := parseLogLine(container, sc.Text())
			select {
			case out <- LogEvent{Line: line}:
			case <-ctx.Done():
				return
			}
		}
		err := sc.Err()
		if ctx.Err() != nil {
			return
		}
		select {
		case out <- LogEvent{Ended: true, Err: err}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

// parseLogLine splits the RFC3339Nano prefix the server adds with
// timestamps=true. Lines without one (continuations) keep a zero time.
func parseLogLine(container, raw string) LogLine {
	ts, rest, ok := strings.Cut(raw, " ")
	if ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			return LogLine{Container: container, Time: t, Text: rest}
		}
	}
	return LogLine{Container: container, Text: raw}
}

// Containers returns a pod's container names: init containers first, then
// regular, then ephemeral.
func Containers(pod *unstructured.Unstructured) []string {
	var names []string
	for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "spec", field)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					names = append(names, n)
				}
			}
		}
	}
	return names
}
