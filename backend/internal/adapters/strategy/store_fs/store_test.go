package store_fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oh-my-opentrade/backend/internal/adapters/strategy/store_fs"
	"github.com/oh-my-opentrade/backend/internal/app/strategy"
	domstrategy "github.com/oh-my-opentrade/backend/internal/domain/strategy"
	portstrategy "github.com/oh-my-opentrade/backend/internal/ports/strategy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestStore_ListAndGetAndLatest(t *testing.T) {
	dir := t.TempDir()
	loadFn := func(path string) (portstrategy.Spec, error) { return strategy.LoadSpecFile(path) }
	s := store_fs.NewStoreWithPollInterval(dir, loadFn, 25*time.Millisecond)

	writeFile(t, dir, "a.toml", `
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.0.0"
name = "a"
description = "d"
author = "system"

[lifecycle]
state = "LiveActive"
paper_only = false

[routing]
symbols = []
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"
exclusive_per_symbol = false

[params]
x = 1
`)

	writeFile(t, dir, "b.toml", `
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.1.0"
name = "b"
description = "d"
author = "system"

[lifecycle]
state = "LiveActive"
paper_only = false

[routing]
symbols = []
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"
exclusive_per_symbol = false

[params]
x = 2
`)

	ctx := context.Background()
	all, err := s.List(ctx, nil)
	require.NoError(t, err)
	require.Len(t, all, 2)

	id, err := domstrategy.NewStrategyID("orb_break_retest")
	require.NoError(t, err)
	v1, err := domstrategy.NewVersion("1.0.0")
	require.NoError(t, err)
	v2, err := domstrategy.NewVersion("1.1.0")
	require.NoError(t, err)

	one, err := s.Get(ctx, id, v1)
	require.NoError(t, err)
	assert.Equal(t, v1, one.Version)
	assert.EqualValues(t, int64(1), one.Params["x"])

	latest, err := s.GetLatest(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, v2, latest.Version)
}

func TestStore_SaveAndReload(t *testing.T) {
	dir := t.TempDir()
	loadFn := func(path string) (portstrategy.Spec, error) { return strategy.LoadSpecFile(path) }
	s := store_fs.NewStoreWithPollInterval(dir, loadFn, 25*time.Millisecond)

	id, err := domstrategy.NewStrategyID("orb_break_retest")
	require.NoError(t, err)
	ver, err := domstrategy.NewVersion("2.0.0")
	require.NoError(t, err)
	cp, err := domstrategy.NewConflictPolicy("priority_wins")
	require.NoError(t, err)

	spec := portstrategy.Spec{
		SchemaVersion: 2,
		ID:            id,
		Version:       ver,
		Name:          "n",
		Description:   "d",
		Author:        "system",
		Lifecycle: portstrategy.LifecycleConfig{
			State:     domstrategy.LifecycleLiveActive,
			PaperOnly: false,
		},
		Routing: portstrategy.RoutingConfig{
			Symbols:            []string{"AAPL"},
			Timeframes:         []string{"1m"},
			Priority:           100,
			ConflictPolicy:     cp,
			ExclusivePerSymbol: false,
		},
		Params: map[string]any{"foo": "bar"},
		Hooks:  map[string]portstrategy.HookRef{},
	}

	require.NoError(t, s.Save(context.Background(), spec))

	got, err := s.Get(context.Background(), id, ver)
	require.NoError(t, err)
	assert.Equal(t, "bar", got.Params["foo"])
}

func TestStore_WatchEmitsOnChange(t *testing.T) {
	dir := t.TempDir()
	loadFn := func(path string) (portstrategy.Spec, error) { return strategy.LoadSpecFile(path) }
	s := store_fs.NewStoreWithPollInterval(dir, loadFn, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.Watch(ctx)
	require.NoError(t, err)

	path := writeFile(t, dir, "a.toml", `
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.0.0"
name = "a"
description = "d"
author = "system"

[lifecycle]
state = "LiveActive"
paper_only = false

[routing]
symbols = []
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"
exclusive_per_symbol = false

[params]
x = 1
`)

	select {
	case <-ch:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected initial watch event")
	}

	time.Sleep(30 * time.Millisecond)

	require.NoError(t, os.WriteFile(path, []byte(`
schema_version = 2

[strategy]
id = "orb_break_retest"
version = "1.0.0"
name = "a"
description = "d"
author = "system"

[lifecycle]
state = "LiveActive"
paper_only = false

[routing]
symbols = []
timeframes = ["1m"]
priority = 100
conflict_policy = "priority_wins"
exclusive_per_symbol = false

[params]
x = 2
`), 0o644))

	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected watch event after change")
	}
}
