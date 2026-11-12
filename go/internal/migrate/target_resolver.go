// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// entityState is the outcome of resolving a quality profile, gate or group
// by name in the target organization.
type entityState int

const (
	entityFound entityState = iota
	// entityMissing means the target answered and holds no such entity.
	entityMissing
	// entityError means the lookup itself failed (transport / HTTP).
	entityError
)

// targetEntityResolver resolves quality profiles, gates and groups by name
// directly in the target SonarQube Cloud organization. The assignment tasks use it
// when the create* output they normally read is absent from the run dir
// because that category was not part of this invocation's --objects (#664:
// a large instance migrated in several runs, each with its own run_id).
// byName is false on the normal path, where no API call is ever made.
//
// Safe for concurrent use: results (including negative ones) are cached per
// entity, and each missing or failing entity is logged once.
type targetEntityResolver struct {
	e      *Executor
	task   string
	byName bool

	mu       sync.Mutex
	profiles map[string]entityState
	gates    map[string]gateResolution
	groups   map[string]*groupLookup
}

type gateResolution struct {
	id    int
	state entityState
}

// groupLookup is one group resolution, shared by every caller asking for
// the same (org, group). state is written before done is closed and only
// read after, so the channel orders the two.
type groupLookup struct {
	done  chan struct{}
	state entityState
}

func newTargetEntityResolver(e *Executor, task string, byName bool) *targetEntityResolver {
	return &targetEntityResolver{
		e: e, task: task, byName: byName,
		profiles: map[string]entityState{},
		gates:    map[string]gateResolution{},
		groups:   map[string]*groupLookup{},
	}
}

// profileState reports whether a quality profile of that name and language
// exists in orgKey. A missing profile is logged once as a WARN.
func (r *targetEntityResolver) profileState(ctx context.Context, orgKey, lang, name string) entityState {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := orgKey + "\x00" + lang + "\x00" + name
	if st, ok := r.profiles[key]; ok {
		return st
	}
	_, err := lookupExistingProfile(ctx, r.e.Raw, name, lang, orgKey)
	st := r.classify(err, "quality profile", name, orgKey, "language", lang)
	r.profiles[key] = st
	return st
}

// gateID returns the ID of the quality gate of that name in orgKey. A
// missing gate is logged once as a WARN.
func (r *targetEntityResolver) gateID(ctx context.Context, orgKey, name string) (int, entityState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := orgKey + "\x00" + name
	if g, ok := r.gates[key]; ok {
		return g.id, g.state
	}
	raw, err := lookupExistingGate(ctx, r.e.Raw, name, orgKey)
	g := gateResolution{state: r.classify(err, "quality gate", name, orgKey)}
	if g.state == entityFound {
		id, convErr := strconv.Atoi(raw)
		if convErr != nil {
			g.state = r.classify(convErr, "quality gate", name, orgKey)
		}
		g.id = id
	}
	r.gates[key] = g
	return g.id, g.state
}

// groupState reports whether a group of that name exists in orgKey. A
// missing group is logged once as a WARN.
//
// Unlike profileState and gateID, the lookup runs outside r.mu: a template
// tranche resolves every group of every template (#685), and holding the
// lock across the HTTP call would run those lookups one at a time. The
// first caller for a key performs the lookup; concurrent callers for the
// same key wait for its result, so each group is still looked up, and
// warned about, exactly once.
func (r *targetEntityResolver) groupState(ctx context.Context, orgKey, name string) entityState {
	key := orgKey + "\x00" + name
	r.mu.Lock()
	if l, ok := r.groups[key]; ok {
		r.mu.Unlock()
		select {
		case <-l.done:
			return l.state
		case <-ctx.Done():
			return entityError
		}
	}
	l := &groupLookup{done: make(chan struct{})}
	r.groups[key] = l
	r.mu.Unlock()

	_, err := lookupExistingGroup(ctx, r.e.Raw, name, orgKey)
	l.state = r.classify(err, "group", name, orgKey)
	close(l.done)
	return l.state
}

func (r *targetEntityResolver) classify(err error, kind, name, orgKey string, attrs ...any) entityState {
	if err == nil {
		return entityFound
	}
	attrs = append([]any{"name", name, "org", orgKey}, attrs...)
	if errors.Is(err, errLookupNotFound) {
		r.e.Logger.Warn(r.task+": the "+kind+" does not exist in the target organization, so it cannot be assigned. "+
			"Migrate it first (include its category in --objects) or create it manually",
			attrs...)
		return entityMissing
	}
	r.e.Logger.Warn(r.task+": could not look up the "+kind+" in the target organization",
		append(attrs, "error", err)...)
	return entityError
}
