package simulator

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const (
	senderStateVersion        = 1
	maxSenderStateBytes       = 32 << 20
	stateDirectoryPermissions = 0700
	stateFilePermissions      = 0600
)

// State keeps the immutable plan, release cursor, and durable delivery receipts
// together. A crash cannot advance generation without preserving its events.
type State struct {
	Version              int    `json:"version"`
	Plan                 Plan   `json:"plan"`
	NextStep             int    `json:"next_step"`
	Delivered            []bool `json:"delivered"`
	Attempts             uint64 `json:"attempts"`
	LostResponseInjected bool   `json:"lost_response_injected"`
	LastError            string `json:"last_error,omitempty"`
}

// Store holds an OS file lock for one writer. The lock automatically releases
// even after SIGKILL; concurrent commands fail rather than overwrite state.
type Store struct {
	path string
	lock *os.File
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), stateDirectoryPermissions); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, stateFilePermissions)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("another simulator command may be using this state: %w", err)
	}

	return &Store{path: path, lock: lock}, nil
}

func (s *Store) Close() error {
	return s.lock.Close()
}

func (s *Store) Load() (*State, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var state State
	if err := decodeStrict(io.LimitReader(file, maxSenderStateBytes), &state); err != nil {
		return nil, fmt.Errorf("invalid sender state; retain the file for investigation: %w", err)
	}

	if err := state.validate(); err != nil {
		return nil, err
	}

	return &state, nil
}

// Save syncs a complete replacement before rename, then syncs its directory.
// Recovery sees the old or new snapshot, never a partially rewritten one.
func (s *Store) Save(state *State) error {
	if err := state.validate(); err != nil {
		return err
	}

	return s.saveJSON(state)
}

// saveJSON preserves either transport or workflow checkpoints using the same
// atomic replacement, file sync, and directory sync guarantees.
func (s *Store) saveJSON(state any) error {
	directory := filepath.Dir(s.path)
	file, err := os.CreateTemp(directory, ".simulator-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(state); err != nil {
		return err
	}

	if err := file.Sync(); err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := os.Rename(file.Name(), s.path); err != nil {
		return err
	}

	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()

	return dir.Sync()
}

func (s *State) validate() error {
	if s.Version != senderStateVersion {
		return fmt.Errorf("unsupported sender state version %d", s.Version)
	}

	if err := s.Plan.validate(); err != nil {
		return fmt.Errorf("invalid saved plan: %w", err)
	}

	if s.NextStep < 0 || s.NextStep > len(s.Plan.Steps) || len(s.Delivered) != len(s.Plan.events()) {
		return fmt.Errorf("invalid sender release cursor or delivery receipts")
	}

	for index := s.released(); index < len(s.Delivered); index++ {
		if s.Delivered[index] {
			return fmt.Errorf("unreleased event %d has a delivery receipt", index)
		}
	}

	return nil
}

func (s *State) released() int {
	count := 0
	for _, step := range s.Plan.Steps[:s.NextStep] {
		count += len(step.Events)
	}

	return count
}

func (s *State) pending(replay bool) []int {
	var indices []int
	for index := range s.released() {
		if replay || !s.Delivered[index] {
			indices = append(indices, index)
		}
	}

	return indices
}
