package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

// tableAccept asks the API server to render objects as a metav1.Table with
// the same columns kubectl prints. The plain JSON fallback keeps older or
// non-conforming servers working.
const tableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"

// Column is one server-defined table column.
type Column struct {
	Name string
	// Priority 0 columns always show. Higher values are shown when there is
	// room, matching kubectl -o wide semantics.
	Priority int32
}

// Row is one object rendered by the server.
type Row struct {
	Name      string
	Namespace string
	UID       string
	Cells     []string
	// Object is the full object when the stream requested includeObject=Object
	// (used for badge predicates); nil otherwise.
	Object *unstructured.Unstructured
}

// Key identifies a row across events. UID is preferred; namespace/name is the
// fallback when the server omits object metadata.
func (r Row) Key() string {
	if r.UID != "" {
		return r.UID
	}
	return r.Namespace + "/" + r.Name
}

// Snapshot is the full current state of a watched resource.
type Snapshot struct {
	Columns []Column
	Rows    []Row
}

// Update is emitted by Stream on every change. Exactly one of Snapshot or Err
// is meaningful; Status always describes the connection.
type Update struct {
	Snapshot Snapshot
	Status   Status
	Err      error
}

// Status describes the watch connection state.
type Status int

const (
	// StatusConnecting means the initial list is in flight.
	StatusConnecting Status = iota
	// StatusLive means the watch is established and rows are current.
	StatusLive
	// StatusReconnecting means the watch dropped and is being reestablished.
	StatusReconnecting
	// StatusError means the last list or watch failed.
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusConnecting:
		return "connecting"
	case StatusLive:
		return "live"
	case StatusReconnecting:
		return "reconnecting"
	default:
		return "error"
	}
}

// Stream lists then watches a resource, emitting a full Snapshot on every
// change until ctx is cancelled. The channel is closed when ctx ends.
// Watch expiry (410 Gone) and disconnects are handled by relisting.
func (c *Client) Stream(ctx context.Context, gvr schema.GroupVersionResource, namespace, fieldSelector, labelSelector string, fullObjects bool) <-chan Update {
	out := make(chan Update, 1)
	go func() {
		defer close(out)
		st := newStore()
		backoff := time.Second
		for {
			if ctx.Err() != nil {
				return
			}
			rv, err := c.list(ctx, gvr, namespace, fieldSelector, labelSelector, fullObjects, st)
			if err != nil {
				if !send(ctx, out, Update{Snapshot: st.snapshot(), Status: StatusError, Err: err}) {
					return
				}
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
			if !send(ctx, out, Update{Snapshot: st.snapshot(), Status: StatusLive}) {
				return
			}

			// Watch from the list's resource version. Returns when the watch
			// closes; relist is true when the server says our RV is too old.
			relist, werr := c.watch(ctx, gvr, namespace, fieldSelector, labelSelector, fullObjects, rv, st, out)
			if ctx.Err() != nil {
				return
			}
			status := StatusReconnecting
			if werr != nil {
				status = StatusError
			}
			if !send(ctx, out, Update{Snapshot: st.snapshot(), Status: status, Err: werr}) {
				return
			}
			if !relist && werr == nil {
				// Clean close; the loop relists anyway, which is cheap enough
				// for M0. Later: resume watch from last RV without a relist.
				continue
			}
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
	return out
}

func (c *Client) list(ctx context.Context, gvr schema.GroupVersionResource, namespace, fieldSelector, labelSelector string, fullObjects bool, st *store) (string, error) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := c.rest.Get().
		AbsPath(resourcePath(gvr, namespace)...).
		SetHeader("Accept", tableAccept)
	if fieldSelector != "" {
		req = req.Param("fieldSelector", fieldSelector)
	}
	if labelSelector != "" {
		req = req.Param("labelSelector", labelSelector)
	}
	if fullObjects {
		req = req.Param("includeObject", "Object")
	}
	raw, err := req.Do(lctx).Raw()
	if err != nil {
		return "", fmt.Errorf("list %s: %w", gvr.Resource, err)
	}
	var t metav1.Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return "", fmt.Errorf("decode %s table: %w", gvr.Resource, err)
	}
	if t.Kind != "Table" {
		return "", fmt.Errorf("server did not return a Table for %s (got %s)", gvr.Resource, t.Kind)
	}
	st.reset(&t)
	return t.ResourceVersion, nil
}

// watch runs one watch connection. It returns relist=true when the server
// reports the resource version is gone, and a non-nil error on failure.
func (c *Client) watch(ctx context.Context, gvr schema.GroupVersionResource, namespace, fieldSelector, labelSelector string, fullObjects bool, rv string, st *store, out chan Update) (relist bool, err error) {
	req := c.rest.Get().
		AbsPath(resourcePath(gvr, namespace)...).
		SetHeader("Accept", tableAccept).
		Param("watch", "true").
		Param("resourceVersion", rv).
		Param("allowWatchBookmarks", "true")
	if fieldSelector != "" {
		req = req.Param("fieldSelector", fieldSelector)
	}
	if labelSelector != "" {
		req = req.Param("labelSelector", labelSelector)
	}
	if fullObjects {
		req = req.Param("includeObject", "Object")
	}
	w, err := req.Watch(ctx)
	if err != nil {
		return false, fmt.Errorf("watch %s: %w", gvr.Resource, err)
	}
	defer w.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return false, nil
			}
			switch ev.Type {
			case watch.Error:
				status, _ := ev.Object.(*metav1.Status)
				if status == nil {
					if u, ok := ev.Object.(*unstructured.Unstructured); ok {
						status = &metav1.Status{}
						_ = fromUnstructured(u, status)
					}
				}
				if status != nil && status.Code == 410 {
					return true, nil
				}
				msg := "unknown watch error"
				if status != nil {
					msg = status.Message
				}
				return true, fmt.Errorf("watch %s: %s", gvr.Resource, msg)
			case watch.Bookmark:
				continue
			case watch.Added, watch.Modified, watch.Deleted:
				u, ok := ev.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				var t metav1.Table
				if err := fromUnstructured(u, &t); err != nil {
					return true, fmt.Errorf("decode watch event: %w", err)
				}
				st.apply(ev.Type, &t)
				if !send(ctx, out, Update{Snapshot: st.snapshot(), Status: StatusLive}) {
					return false, nil
				}
			}
		}
	}
}

func fromUnstructured(u *unstructured.Unstructured, into any) error {
	b, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// send delivers u unless ctx is done. It reports whether delivery happened.
// The channel has capacity 1. If the consumer has not drained the previous
// update yet, that stale value is discarded in favor of the fresher one:
// snapshots are complete, so nothing is lost and the watch never blocks on
// a slow UI.
func send(ctx context.Context, out chan Update, u Update) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case out <- u:
			return true
		default:
			select {
			case <-out:
			default:
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// store holds the current rows keyed by identity.
type store struct {
	columns []Column
	rows    map[string]Row
}

func newStore() *store { return &store{rows: map[string]Row{}} }

func (s *store) reset(t *metav1.Table) {
	s.setColumns(t)
	s.rows = make(map[string]Row, len(t.Rows))
	for i := range t.Rows {
		r := toRow(&t.Rows[i])
		s.rows[r.Key()] = r
	}
}

func (s *store) apply(typ watch.EventType, t *metav1.Table) {
	s.setColumns(t)
	for i := range t.Rows {
		r := toRow(&t.Rows[i])
		if typ == watch.Deleted {
			delete(s.rows, r.Key())
		} else {
			s.rows[r.Key()] = r
		}
	}
}

// setColumns records column definitions when present. Watch events after the
// first may omit them.
func (s *store) setColumns(t *metav1.Table) {
	if len(t.ColumnDefinitions) == 0 {
		return
	}
	cols := make([]Column, len(t.ColumnDefinitions))
	for i, c := range t.ColumnDefinitions {
		cols[i] = Column{Name: c.Name, Priority: c.Priority}
	}
	s.columns = cols
}

func (s *store) snapshot() Snapshot {
	rows := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	cols := make([]Column, len(s.columns))
	copy(cols, s.columns)
	return Snapshot{Columns: cols, Rows: rows}
}

func toRow(tr *metav1.TableRow) Row {
	r := Row{Cells: make([]string, len(tr.Cells))}
	for i, c := range tr.Cells {
		r.Cells[i] = formatCell(c)
	}
	if len(tr.Object.Raw) > 0 {
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(tr.Object.Raw); err == nil {
			r.Name = u.GetName()
			r.Namespace = u.GetNamespace()
			r.UID = string(u.GetUID())
			// Keep the full object only when it is one (not PartialObjectMetadata).
			if u.GetKind() != "PartialObjectMetadata" {
				r.Object = u
			}
		}
	}
	if r.Name == "" && len(r.Cells) > 0 {
		r.Name = r.Cells[0]
	}
	return r
}

func formatCell(v any) string {
	switch x := v.(type) {
	case nil:
		return "<none>"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}
