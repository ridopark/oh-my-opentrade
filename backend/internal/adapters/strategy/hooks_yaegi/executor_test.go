package hooks_yaegi_test

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	hooks_yaegi "github.com/oh-my-opentrade/backend/internal/adapters/strategy/hooks_yaegi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSandbox_BlockedImport(t *testing.T) {
	sb := hooks_yaegi.NewSandbox()

	src := `package hookpkg

import "os"

func MyHook(params map[string]any, bar map[string]any) (map[string]any, error) {
    _ = os.Args
    return map[string]any{"ok": true}, nil
}`

	_, err := sb.Compile("MyHook", src)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrBlockedImport)
}

func TestSandbox_AllowedImport(t *testing.T) {
	sb := hooks_yaegi.NewSandbox()

	src := `package hookpkg

import "math"

func MyHook(params map[string]any, bar map[string]any) (map[string]any, error) {
    v := math.Sqrt(params["value"].(float64))
    return map[string]any{"result": v}, nil
}`

	fn, err := sb.Compile("MyHook", src)
	require.NoError(t, err)

	out, err := fn(map[string]any{"value": 81.0}, map[string]any{})
	require.NoError(t, err)
	assert.Equal(t, 9.0, out["result"])
}

func TestSandbox_WrongPackageName(t *testing.T) {
	sb := hooks_yaegi.NewSandbox()

	src := `package foo

func MyHook(params map[string]any, bar map[string]any) (map[string]any, error) {
    return map[string]any{"ok": true}, nil
}`

	_, err := sb.Compile("MyHook", src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hookpkg")
}

func TestSandbox_WrongFunctionSignature(t *testing.T) {
	sb := hooks_yaegi.NewSandbox()

	src := `package hookpkg

func MyHook() string {
    return "nope"
}`

	_, err := sb.Compile("MyHook", src)
	require.Error(t, err)
}

func TestSandbox_HookReceivesParamsAndBar(t *testing.T) {
	sb := hooks_yaegi.NewSandbox()

	src := `package hookpkg

func MyHook(params map[string]any, bar map[string]any) (map[string]any, error) {
    return map[string]any{
        "threshold": params["threshold"],
        "close": bar["close"],
    }, nil
}`

	fn, err := sb.Compile("MyHook", src)
	require.NoError(t, err)

	params := map[string]any{"threshold": 1.23}
	bar := map[string]any{"close": 456.78}

	out, err := fn(params, bar)
	require.NoError(t, err)
	assert.Equal(t, 1.23, out["threshold"])
	assert.Equal(t, 456.78, out["close"])
}

func TestExecutor_Timeout(t *testing.T) {
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		time.Sleep(5 * time.Second)
		return map[string]any{"ok": true}, nil
	})

	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithTimeout(10*time.Millisecond))
	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrHookTimeout)
	assert.Equal(t, uint64(1), e.Timeouts())
}

func TestExecutor_CircuitBreakerTrips(t *testing.T) {
	fnErr := errors.New("boom")
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		return nil, fnErr
	})
	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithMaxFailures(3), hooks_yaegi.WithCooldown(200*time.Millisecond))

	for i := 0; i < 3; i++ {
		_, err := e.Execute(nil, nil, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fnErr)
		assert.False(t, errors.Is(err, hooks_yaegi.ErrCircuitOpen))
	}

	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrCircuitOpen)
	assert.Equal(t, uint64(1), e.CircuitTrips())
}

func TestExecutor_CircuitBreakerReset(t *testing.T) {
	fnErr := errors.New("boom")
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		return nil, fnErr
	})
	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithMaxFailures(3), hooks_yaegi.WithCooldown(200*time.Millisecond))

	for i := 0; i < 3; i++ {
		_, err := e.Execute(nil, nil, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fnErr)
	}
	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrCircuitOpen)

	e.Reset()
	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	assert.False(t, errors.Is(err, hooks_yaegi.ErrCircuitOpen))
}

func TestExecutor_CircuitBreakerCooldown(t *testing.T) {
	fnErr := errors.New("boom")
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		return nil, fnErr
	})

	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithMaxFailures(1), hooks_yaegi.WithCooldown(50*time.Millisecond))

	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrCircuitOpen)

	time.Sleep(60 * time.Millisecond)
	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	assert.False(t, errors.Is(err, hooks_yaegi.ErrCircuitOpen))
}

func TestExecutor_SuccessResetsConsecutive(t *testing.T) {
	var i int
	fnErr := errors.New("boom")
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		i++
		switch i {
		case 1, 2:
			return nil, fnErr
		case 3:
			return map[string]any{"ok": true}, nil
		case 4, 5:
			return nil, fnErr
		case 6:
			return map[string]any{"ok": true}, nil
		default:
			return map[string]any{"ok": true}, nil
		}
	})

	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithMaxFailures(3), hooks_yaegi.WithCooldown(200*time.Millisecond))

	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)

	out, err := e.Execute(nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, true, out["ok"])

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)

	out, err = e.Execute(nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, true, out["ok"])

	assert.Equal(t, uint64(0), e.CircuitTrips())
}

func TestExecutor_Metrics(t *testing.T) {
	var step atomic.Int64
	boom1 := errors.New("boom1")
	boom2 := errors.New("boom2")
	boom3 := errors.New("boom3")
	fn := hooks_yaegi.HookFunc(func(params map[string]any, bar map[string]any) (map[string]any, error) {
		cur := step.Add(1)
		switch cur {
		case 1:
			time.Sleep(50 * time.Millisecond)
			return map[string]any{"never": true}, nil
		case 2:
			return nil, boom1
		case 3:
			return nil, boom2
		case 4:
			return nil, boom3
		default:
			return map[string]any{"ok": true}, nil
		}
	})

	e := hooks_yaegi.NewHookExecutor(fn, hooks_yaegi.WithTimeout(10*time.Millisecond), hooks_yaegi.WithMaxFailures(3), hooks_yaegi.WithCooldown(200*time.Millisecond))

	_, err := e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrHookTimeout)

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom1)

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom2)

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, hooks_yaegi.ErrCircuitOpen)

	e.Reset()

	_, err = e.Execute(nil, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom3)

	out, err := e.Execute(nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, true, out["ok"])

	assert.Equal(t, uint64(6), e.TotalCalls())
	assert.Equal(t, uint64(3), e.Failures())
	assert.Equal(t, uint64(1), e.Timeouts())
	assert.Equal(t, uint64(1), e.CircuitTrips())
}
