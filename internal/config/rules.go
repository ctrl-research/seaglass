package config

import (
	"fmt"
	"strings"
	"time"
)

// Ruleset is the operator-support configuration: declarative actions,
// jumps, badges, and commands. Presets ship embedded; users add or
// override rules in seaglass.yaml. Every rule runs through the same
// patch/get/watch paths as the built-in features.
type Ruleset struct {
	Actions  []ActionRule  `yaml:"actions"`
	Jumps    []JumpRule    `yaml:"jumps"`
	Badges   []BadgeRule   `yaml:"badges"`
	Commands []CommandRule `yaml:"commands"`
	Groups   []GroupRule   `yaml:"groups"`
}

// Match selects the resource types a rule applies to. Empty fields match
// anything; Group accepts a trailing-or-leading "*" glob (e.g.
// "*.toolkit.fluxcd.io"). Resource is the plural name; Kind the singular.
type Match struct {
	Group    string `yaml:"group,omitempty"`
	Resource string `yaml:"resource,omitempty"`
	Kind     string `yaml:"kind,omitempty"`
}

// Matches reports whether a resource (by group, plural resource, kind)
// satisfies the match.
func (m Match) Matches(group, resource, kind string) bool {
	if !globMatch(m.Group, group) {
		return false
	}
	if m.Resource != "" && !strings.EqualFold(m.Resource, resource) {
		return false
	}
	if m.Kind != "" && !strings.EqualFold(m.Kind, kind) {
		return false
	}
	return true
}

// empty reports whether the match selects everything.
func (m Match) empty() bool {
	return m.Group == "" && m.Resource == "" && m.Kind == ""
}

// globMatch supports "", exact, "prefix*", and "*suffix".
func globMatch(pattern, value string) bool {
	switch {
	case pattern == "":
		return true
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(value, pattern[1:])
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(value, pattern[:len(pattern)-1])
	default:
		return strings.EqualFold(pattern, value)
	}
}

// Duration is a time.Duration that unmarshals from a YAML string like
// "60s" or "2m".
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// ActionRule is a declarative action: patch a set of fields (or delete),
// optionally after a confirm or an input, optionally waiting for a status
// predicate. Patch is a nested partial object (a JSON merge patch); string
// leaves are templated with {{now}}, {{.input}}, and {{.spec...}} paths.
type ActionRule struct {
	Name    string         `yaml:"name"`
	Desc    string         `yaml:"desc,omitempty"`
	Key     string         `yaml:"key,omitempty"`
	Match   Match          `yaml:"match"`
	Confirm bool           `yaml:"confirm,omitempty"`
	Force   bool           `yaml:"force,omitempty"`
	Follow  bool           `yaml:"follow,omitempty"`
	Delete  bool           `yaml:"delete,omitempty"`
	Patch   map[string]any `yaml:"patch,omitempty"`
	Input   *InputRule     `yaml:"input,omitempty"`
	Wait    *WaitRule      `yaml:"wait,omitempty"`
}

// InputRule prompts for a value before running. Default is a template;
// Pattern is an optional validation regex.
type InputRule struct {
	Label   string `yaml:"label"`
	Default string `yaml:"default,omitempty"`
	Pattern string `yaml:"pattern,omitempty"`
}

// WaitRule watches the patched object until a status field equals a value,
// or a condition reaches a status, or the timeout elapses.
type WaitRule struct {
	Field     string   `yaml:"field,omitempty"`
	Equals    string   `yaml:"equals,omitempty"`
	Condition string   `yaml:"condition,omitempty"`
	Status    string   `yaml:"status,omitempty"`
	Timeout   Duration `yaml:"timeout,omitempty"`
}

// JumpRule navigates from a source object to a related target. From
// describes where the reference lives; To constrains the target kind.
type JumpRule struct {
	Name  string   `yaml:"name"`
	Match Match    `yaml:"match"`
	From  JumpFrom `yaml:"from"`
	To    Match    `yaml:"to,omitempty"`
}

// JumpFrom is one of: a label pair (navigate to objects of To's kind whose
// namespace/name equal these label values), a label selector on the source
// used to list target pods, or a spec field holding a {kind,name,namespace}
// reference.
type JumpFrom struct {
	// NameLabel/NamespaceLabel: the target's name/namespace come from these
	// labels on the source.
	NameLabel      string `yaml:"nameLabel,omitempty"`
	NamespaceLabel string `yaml:"namespaceLabel,omitempty"`
	// Selector: a dotted path to a label selector map on the source; targets
	// are objects (usually pods) matching it.
	Selector string `yaml:"selector,omitempty"`
	// Field: a dotted path to a reference object with kind/name/namespace.
	Field string `yaml:"field,omitempty"`
	// List: a dotted path to a list of references; each item is decoded with
	// Ref into an object to list in a browsable table. This generalizes
	// Flux's status.inventory to any operator that exposes a managed-object
	// list.
	List string   `yaml:"list,omitempty"`
	Ref  *RefSpec `yaml:"ref,omitempty"`
}

// RefSpec describes how to read an object reference from a list item. Use
// Field+Format for a packed string (Flux inventory: the "id" field packs
// namespace/name/group/kind joined by "_"); otherwise name the sub-fields
// holding each part.
type RefSpec struct {
	// Packed form: the item field holding a delimited string, the order of
	// its parts (a Sep-joined list of namespace/name/group/kind), and the
	// separator (default "_").
	Field  string `yaml:"field,omitempty"`
	Format string `yaml:"format,omitempty"`
	Sep    string `yaml:"sep,omitempty"`
	// Structured form: sub-field paths within each item.
	Kind      string `yaml:"kind,omitempty"`
	Name      string `yaml:"name,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
	Group     string `yaml:"group,omitempty"`
}

// BadgeRule colors a row (and tags it) when a predicate holds.
type BadgeRule struct {
	Name  string    `yaml:"name"`
	Match Match     `yaml:"match"`
	When  Predicate `yaml:"when"`
	Style string    `yaml:"style"` // ok | warning | error | muted
	Tag   string    `yaml:"tag,omitempty"`
}

// Predicate is a condition status test or a field-equals test.
type Predicate struct {
	Condition string `yaml:"condition,omitempty"`
	Status    string `yaml:"status,omitempty"`
	Field     string `yaml:"field,omitempty"`
	Equals    string `yaml:"equals,omitempty"`
}

func (p Predicate) empty() bool {
	return p.Condition == "" && p.Field == ""
}

// GroupRule defines a merged multi-kind table opened from the palette. Its
// members are every discovered resource matching Match, or the explicit
// Kinds list. Used for the flux and workloads groups; any operator can add
// one in config.
type GroupRule struct {
	Name  string  `yaml:"name"`
	Desc  string  `yaml:"desc,omitempty"`
	Match Match   `yaml:"match,omitempty"`
	Kinds []Match `yaml:"kinds,omitempty"`
}

// MemberMatch reports whether a resource belongs to the group.
func (g GroupRule) MemberMatch(group, resource, kind string) bool {
	if len(g.Kinds) > 0 {
		for _, k := range g.Kinds {
			if k.Matches(group, resource, kind) {
				return true
			}
		}
		return false
	}
	return !g.Match.empty() && g.Match.Matches(group, resource, kind)
}

// CommandRule runs an external program with the selected object's fields in
// the environment. The escape hatch; explicitly the unsafe path.
type CommandRule struct {
	Name    string   `yaml:"name"`
	Desc    string   `yaml:"desc,omitempty"`
	Key     string   `yaml:"key,omitempty"`
	Match   Match    `yaml:"match"`
	Command []string `yaml:"command"`
	Confirm bool     `yaml:"confirm,omitempty"`
}

var badgeStyles = map[string]bool{"ok": true, "warning": true, "error": true, "muted": true}

// Validate checks a ruleset for semantic errors, prefixing each with the
// source (e.g. a preset name or the user config path).
func (r Ruleset) Validate(source string) error {
	var errs []string
	add := func(format string, args ...any) {
		errs = append(errs, source+": "+fmt.Sprintf(format, args...))
	}
	for i, a := range r.Actions {
		where := fmt.Sprintf("action[%d]", i)
		if a.Name != "" {
			where = "action " + a.Name
		}
		if a.Name == "" {
			add("%s: name is required", where)
		}
		if a.Match.empty() {
			add("%s: match must set at least one of group/resource/kind", where)
		}
		if !a.Delete && len(a.Patch) == 0 {
			add("%s: must set patch or delete: true", where)
		}
		if a.Delete && len(a.Patch) > 0 {
			add("%s: cannot set both patch and delete", where)
		}
		if a.Input != nil && a.Input.Label == "" {
			add("%s: input.label is required", where)
		}
	}
	for i, j := range r.Jumps {
		where := fmt.Sprintf("jump[%d]", i)
		if j.Name != "" {
			where = "jump " + j.Name
		}
		if j.Name == "" {
			add("%s: name is required", where)
		}
		n := 0
		if j.From.NameLabel != "" {
			n++
		}
		if j.From.Selector != "" {
			n++
		}
		if j.From.Field != "" {
			n++
		}
		if j.From.List != "" {
			n++
			if j.From.Ref == nil {
				add("%s: from.list requires a ref", where)
			} else if j.From.Ref.Field == "" && j.From.Ref.Kind == "" {
				add("%s: from.ref needs field+format (packed) or kind/name subfields", where)
			}
		}
		if n != 1 {
			add("%s: from must set exactly one of nameLabel, selector, field, or list", where)
		}
	}
	for i, b := range r.Badges {
		where := fmt.Sprintf("badge[%d]", i)
		if b.Name != "" {
			where = "badge " + b.Name
		}
		if b.Name == "" {
			add("%s: name is required", where)
		}
		if b.When.empty() {
			add("%s: when must set a condition or a field", where)
		}
		if !badgeStyles[b.Style] {
			add("%s: style %q must be one of ok, warning, error, muted", where, b.Style)
		}
	}
	for i, g := range r.Groups {
		where := fmt.Sprintf("group[%d]", i)
		if g.Name != "" {
			where = "group " + g.Name
		}
		if g.Name == "" {
			add("%s: name is required", where)
		}
		if g.Match.empty() && len(g.Kinds) == 0 {
			add("%s: set match or a non-empty kinds list", where)
		}
	}
	for i, c := range r.Commands {
		where := fmt.Sprintf("command[%d]", i)
		if c.Name != "" {
			where = "command " + c.Name
		}
		if c.Name == "" {
			add("%s: name is required", where)
		}
		if len(c.Command) == 0 {
			add("%s: command is required", where)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// Merge appends another ruleset's rules onto this one. Later rules win when
// engines resolve by name, so user rules (merged last) override presets.
func (r *Ruleset) Merge(other Ruleset) {
	r.Actions = append(r.Actions, other.Actions...)
	r.Jumps = append(r.Jumps, other.Jumps...)
	r.Badges = append(r.Badges, other.Badges...)
	r.Commands = append(r.Commands, other.Commands...)
	r.Groups = append(r.Groups, other.Groups...)
}
