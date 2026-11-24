package tui

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	tea "github.com/charmbracelet/bubbletea"
)

// Sync panel messages

// syncStateLoadedMsg is sent when the sync state is loaded.
type syncStateLoadedMsg struct {
	state *sync.SyncState
	err   error
}

// syncMachineAddedMsg is sent when a machine is added.
type syncMachineAddedMsg struct {
	machine *sync.Machine
	err     error
}

// syncMachineRemovedMsg is sent when a machine is removed.
type syncMachineRemovedMsg struct {
	machineID string
	err       error
}

// syncMachineUpdatedMsg is sent when a machine is updated.
type syncMachineUpdatedMsg struct {
	machine *sync.Machine
	err     error
}

// syncTestResultMsg is sent when a connection test completes.
type syncTestResultMsg struct {
	machineID string
	success   bool
	message   string
	err       error
}

// syncStartedMsg is sent when a sync operation starts.
type syncStartedMsg struct {
	machineID   string
	machineName string
}

// syncCompletedMsg is sent when a sync operation completes.
type syncCompletedMsg struct {
	machineID   string
	machineName string
	stats       sync.SyncStats
	err         error
}

// loadSyncState loads the sync state from disk.
func (m Model) loadSyncState() tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		return syncStateLoadedMsg{state: state, err: err}
	}
}

// LoadSyncStateCmd returns a command that loads the sync state.
func LoadSyncStateCmd() tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		return syncStateLoadedMsg{state: state, err: err}
	}
}

// addSyncMachine adds a new machine to the pool.
func (m Model) addSyncMachine(name, address, portStr, user, keyPath string) tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		if err != nil {
			return syncMachineAddedMsg{err: err}
		}

		machine := sync.NewMachine(name, address)

		// Parse port
		if portStr != "" {
			if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
				machine.Port = port
			}
		}

		machine.SSHUser = user
		machine.SSHKeyPath = keyPath
		machine.Source = sync.SourceManual

		if err := state.Pool.AddMachine(machine); err != nil {
			return syncMachineAddedMsg{err: err}
		}

		if err := state.Save(); err != nil {
			return syncMachineAddedMsg{err: err}
		}

		return syncMachineAddedMsg{machine: machine}
	}
}

// removeSyncMachine removes a machine from the pool.
func (m Model) removeSyncMachine(machineID string) tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		if err != nil {
			return syncMachineRemovedMsg{machineID: machineID, err: err}
		}

		if err := state.Pool.RemoveMachine(machineID); err != nil {
			return syncMachineRemovedMsg{machineID: machineID, err: err}
		}

		if err := state.Save(); err != nil {
			return syncMachineRemovedMsg{machineID: machineID, err: err}
		}

		return syncMachineRemovedMsg{machineID: machineID}
	}
}

// updateSyncMachine updates a machine in the pool.
func (m Model) updateSyncMachine(machineID, name, address, portStr, user, keyPath string) tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		if err != nil {
			return syncMachineUpdatedMsg{err: err}
		}

		machine := state.Pool.GetMachine(machineID)
		if machine == nil {
			return syncMachineUpdatedMsg{err: &sync.ValidationError{Field: "id", Message: "machine not found"}}
		}

		// Update fields
		machine.Name = name
		machine.Address = address
		if portStr != "" {
			if port, err := strconv.Atoi(portStr); err == nil && port > 0 {
				machine.Port = port
			}
		}
		machine.SSHUser = user
		machine.SSHKeyPath = keyPath

		if err := state.Pool.UpdateMachine(machine); err != nil {
			return syncMachineUpdatedMsg{err: err}
		}

		if err := state.Save(); err != nil {
			return syncMachineUpdatedMsg{err: err}
		}

		return syncMachineUpdatedMsg{machine: machine}
	}
}

// testSyncMachine tests connectivity to a machine.
func (m Model) testSyncMachine(machineID string) tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		if err != nil {
			return syncTestResultMsg{machineID: machineID, err: err}
		}

		machine := state.Pool.GetMachine(machineID)
		if machine == nil {
			return syncTestResultMsg{
				machineID: machineID,
				success:   false,
				message:   "Machine not found",
			}
		}

		// Test actual SSH connectivity
		result := sync.TestMachineConnectivity(machine, sync.DefaultConnectOptions())
		msg := ""
		if result.Success {
			msg = fmt.Sprintf("Connected (latency: %v, SFTP: %v)", result.Latency, result.SFTPWorks)
		} else if result.Error != nil {
			msg = result.Error.Error()
		}
		return syncTestResultMsg{
			machineID: machineID,
			success:   result.Success,
			message:   msg,
			err:       result.Error,
		}
	}
}

// syncWithMachine runs a sync against a single machine.
func (m Model) syncWithMachine(machineID string) tea.Cmd {
	return func() tea.Msg {
		state, err := sync.LoadSyncState()
		if err != nil {
			return syncCompletedMsg{machineID: machineID, err: err}
		}

		machine := state.Pool.GetMachine(machineID)
		if machine == nil {
			return syncCompletedMsg{machineID: machineID, err: fmt.Errorf("machine not found")}
		}

		syncer, err := sync.NewSyncer(sync.DefaultSyncerConfig())
		if err != nil {
			return syncCompletedMsg{machineID: machineID, machineName: machine.Name, err: err}
		}
		defer syncer.Close()

		results, err := syncer.SyncWithMachine(context.Background(), machine)
		if err != nil {
			return syncCompletedMsg{machineID: machineID, machineName: machine.Name, err: err}
		}

		return syncCompletedMsg{
			machineID:   machineID,
			machineName: machine.Name,
			stats:       sync.AggregateResults(results),
		}
	}
}
