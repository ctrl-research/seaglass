package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/rest"
)

// Resource describes one listable, watchable API resource.
type Resource struct {
	GVR        schema.GroupVersionResource `json:"gvr"`
	Kind       string                      `json:"kind"`
	Namespaced bool                        `json:"namespaced"`
	ShortNames []string                    `json:"shortNames,omitempty"`
}

// Pods is the default resource shown on startup.
var Pods = Resource{
	GVR:        schema.GroupVersionResource{Version: "v1", Resource: "pods"},
	Kind:       "Pod",
	Namespaced: true,
	ShortNames: []string{"po"},
}

// Events is the core events resource.
var Events = Resource{
	GVR:        schema.GroupVersionResource{Version: "v1", Resource: "events"},
	Kind:       "Event",
	Namespaced: true,
	ShortNames: []string{"ev"},
}

// Namespaces is the namespace resource, used by the palette.
var Namespaces = Resource{
	GVR:        schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
	Kind:       "Namespace",
	ShortNames: []string{"ns"},
}

// Name is the resource's plural name, for display.
func (r Resource) Name() string { return r.GVR.Resource }

// GroupVersion is "v1" for core resources and "group/version" otherwise.
func (r Resource) GroupVersion() string {
	if r.GVR.Group == "" {
		return r.GVR.Version
	}
	return r.GVR.Group + "/" + r.GVR.Version
}

// Resources returns every resource on the server that supports list and
// watch, preferred version per group, core group first then alphabetical.
// Results are cached on disk under the user cache dir for an hour.
func (c *Client) Resources(ctx context.Context) ([]Resource, error) {
	cacheRoot, err := cacheDir()
	if err != nil {
		return nil, err
	}
	host := sanitize(c.cfg.Host)
	dc, err := disk.NewCachedDiscoveryClientForConfig(
		rest.CopyConfig(c.cfg),
		filepath.Join(cacheRoot, "discovery", host),
		filepath.Join(cacheRoot, "http", host),
		time.Hour,
	)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}

	lists, err := discovery.ServerPreferredResources(dc)
	if err != nil && len(lists) == 0 {
		// Partial results are normal when an aggregated API is down; only
		// fail when we got nothing at all.
		return nil, fmt.Errorf("discover resources: %w", err)
	}

	var out []Resource
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") {
				continue // subresource
			}
			if !slices.Contains(r.Verbs, "list") || !slices.Contains(r.Verbs, "watch") {
				continue
			}
			out = append(out, Resource{
				GVR:        gv.WithResource(r.Name),
				Kind:       r.Kind,
				Namespaced: r.Namespaced,
				ShortNames: r.ShortNames,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := out[i].GVR.Group == "", out[j].GVR.Group == ""
		if ci != cj {
			return ci
		}
		if out[i].GVR.Resource != out[j].GVR.Resource {
			return out[i].GVR.Resource < out[j].GVR.Resource
		}
		return out[i].GVR.Group < out[j].GVR.Group
	})
	return out, nil
}

// NamespaceNames lists namespace names in the cluster.
func (c *Client) NamespaceNames(ctx context.Context) ([]string, error) {
	st := newStore()
	if _, err := c.list(ctx, Namespaces.GVR, "", "", "", false, st); err != nil {
		return nil, err
	}
	snap := st.snapshot()
	names := make([]string, len(snap.Rows))
	for i, r := range snap.Rows {
		names[i] = r.Name
	}
	return names, nil
}

func cacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "seaglass"), nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "seaglass"), nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(host string) string {
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return strings.Trim(unsafeChars.ReplaceAllString(host, "_"), "_")
}
