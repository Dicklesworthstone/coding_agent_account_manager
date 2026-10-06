package exec

import (
	"context"
	"errors"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

type preflightProvider struct {
	mockProvider
	calls         int
	commandChosen bool
	prepareErr    error
}

func (p *preflightProvider) PrepareRun(context.Context, *profile.Profile) error {
	p.calls++
	return p.prepareErr
}

func (p *preflightProvider) DefaultBin() string {
	p.commandChosen = true
	return p.mockProvider.DefaultBin()
}

func TestRunRequiredProfilePreflight(t *testing.T) {
	for _, smart := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "smart-claude"}[smart], func(t *testing.T) {
			failure := errors.New("malformed Claude settings")
			mock := &preflightProvider{
				mockProvider: mockProvider{id: "claude", defaultBin: "true"},
				prepareErr:   failure,
			}
			prof := &profile.Profile{Name: "test", Provider: "claude", BasePath: t.TempDir()}
			runner := NewRunner(provider.NewRegistry())
			run := runner.Run
			if smart {
				run = NewSmartRunner(runner, SmartRunnerOptions{}).Run
			}
			err := run(context.Background(), RunOptions{Profile: prof, Provider: mock, NoLock: true})
			if !errors.Is(err, failure) || mock.calls != 1 || mock.commandChosen {
				t.Fatalf("required preflight was bypassed: err=%v calls=%d command=%v", err, mock.calls, mock.commandChosen)
			}
		})
	}
}

func TestRunSkipsRequiredProfilePreflightForGlobalEnv(t *testing.T) {
	mock := &preflightProvider{
		mockProvider: mockProvider{id: "test", defaultBin: "true"},
		prepareErr:   errors.New("must not run for global vault execution"),
	}
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: t.TempDir()}
	err := NewRunner(provider.NewRegistry()).Run(context.Background(), RunOptions{
		Profile: prof, Provider: mock, NoLock: true, UseGlobalEnv: true,
	})
	if err != nil || mock.calls != 0 {
		t.Fatalf("global preflight: err=%v calls=%d", err, mock.calls)
	}
}

var _ provider.ProfileRunPreparer = (*preflightProvider)(nil)
